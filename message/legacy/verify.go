// SPDX-License-Identifier: MIT

package legacy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"strings"

	"github.com/pkg/errors"
	refs "go.mindeco.de/ssb-refs"
	"golang.org/x/crypto/nacl/auth"
)

// ExtractSignature expects a pretty printed message and uses a regexp to strip it from the msg for signature verification
func ExtractSignature(b []byte) ([]byte, Signature, error) {
	// BUG(cryptix): this expects signature on the root of the object.
	// some functions (like createHistoryStream with keys:true) nest the message on level deeper and this fails
	matches := signatureRegexp.FindSubmatch(b)
	if n := len(matches); n != 2 {
		return nil, "", errors.Errorf("message Encode: expected signature in formatted bytes. Only %d matches", n)
	}
	sig := Signature(matches[1])
	out := signatureRegexp.ReplaceAll(b, []byte{})
	return out, sig, nil
}

// Verify takes an slice of bytes (like json.RawMessage) and uses EncodePreserveOrder to pretty print it.
// It then uses ExtractSignature and verifies the found signature against the author field of the message.
// If hmacSecret is non nil, it uses that as the Key for NACL crypto_auth() and verifies the signature against the hash of the message.
// At last it uses internalV8Binary to create a the SHA256 hash for the message key.
// If you find a buggy message, use `node ./encode_test.js $feedID` to generate a new testdata.zip
func Verify(raw []byte, hmacSecret *[32]byte) (*refs.MessageRef, *DeserializedMessage, error) {
	enc, err := EncodePreserveOrder(raw)
	if err != nil {
		if len(raw) > 15 {
			raw = raw[:15]
		}
		return nil, nil, errors.Wrapf(err, "ssb Verify: could not encode message: %q...", raw)
	}

	// destroys it for the network layer but makes it easier to access its values
	var dmsg DeserializedMessage
	if err := json.Unmarshal(raw, &dmsg); err != nil {
		if len(raw) > 15 {
			raw = raw[:15]
		}
		return nil, nil, errors.Wrapf(err, "ssb Verify: could not json.Unmarshal message: %q...", raw)
	}

	if dmsg.Hash != "sha256" {
		return nil, nil, errors.Errorf("ssb Verify: scuttlebutt happend anyway")
	}

	if n := len(dmsg.Content); n < 1 {
		return nil, nil, errors.Errorf("ssb Verify: has no content (%d)", n)
	}

	switch dmsg.Content[0] {
	case '{':
		var typedContent struct {
			Type string
		}
		err = json.Unmarshal(dmsg.Content, &typedContent)
		if err != nil {
			return nil, nil, err
		}

		if tlen := len(typedContent.Type); tlen < 3 || tlen > 53 {
			return nil, nil, errors.Errorf("ssb Verify: scuttlebutt v1 requires a type field: %q", typedContent.Type)
		}

	case '"':
		var justString string
		err = json.Unmarshal(dmsg.Content, &justString)
		if err != nil {
			return nil, nil, err
		}

		if !strings.HasSuffix(justString, ".box") && !strings.HasSuffix(justString, ".box2") {
			return nil, nil, errors.Errorf("ssb Verify: scuttlebutt v1 private messages need to have the right suffix")
		}

	default:
		return nil, nil, errors.Errorf("ssb Verify: unexpected content: %q", dmsg.Content[0])
	}

	woSig, sig, err := ExtractSignature(enc)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "ssb Verify(%s:%d): could not extract signature", dmsg.Author.Ref(), dmsg.Sequence)
	}

	if hmacSecret != nil {
		mac := auth.Sum(woSig, hmacSecret)
		woSig = mac[:]
	}

	if err := sig.Verify(woSig, &dmsg.Author); err != nil {
		return nil, nil, errors.Wrapf(err, "ssb Verify(%s:%d): could not verify message", dmsg.Author.Ref(), dmsg.Sequence)
	}

	// hash the message - it's sadly the internal string rep of v8 that get's hashed, not the json string
	v8warp, err := InternalV8Binary(enc)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "ssb Verify(%s:%d): could hash convert message", dmsg.Author.Ref(), dmsg.Sequence)
	}
	h := sha256.New()
	io.Copy(h, bytes.NewReader(v8warp))

	mr := refs.MessageRef{
		Hash: h.Sum(nil),
		Algo: refs.RefAlgoMessageSSB1,
	}
	return &mr, &dmsg, nil
}
