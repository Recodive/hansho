package crypto

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"

	"github.com/recodive/hansho/hub/internal/brand"
)

var b64 = base64.RawURLEncoding

// Envelope is the encrypted, signed message carried in the APNs payload ("c").
type Envelope struct {
	V  int    `json:"v"`
	P  string `json:"p"`
	M  string `json:"m"`
	CT string `json:"ct"`
	S  string `json:"s"`
}

// MessageKey derives the per-pairing message encryption key.
func MessageKey(pairingKey []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, pairingKey, nil, brand.Label("v1/msg"), 32)
}

func envelopeAAD(pairingID, messageID string) []byte {
	return []byte("v1\n" + pairingID + "\n" + messageID)
}

func envelopeSigned(pairingID, messageID, ctB64 string) []byte {
	return []byte("v1\n" + pairingID + "\n" + messageID + "\n" + ctB64)
}

// SealMessage encrypts plaintext under the pairing key and signs the result with the hub key.
func SealMessage(hubPriv ed25519.PrivateKey, pairingKey []byte, pairingID, messageID string, plaintext []byte) (Envelope, error) {
	return sealMessage(rand.Reader, hubPriv, pairingKey, pairingID, messageID, plaintext)
}

func sealMessage(r io.Reader, hubPriv ed25519.PrivateKey, pairingKey []byte, pairingID, messageID string, plaintext []byte) (Envelope, error) {
	ek, err := MessageKey(pairingKey)
	if err != nil {
		return Envelope{}, err
	}
	aead, err := newGCM(ek)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return Envelope{}, err
	}
	ct := aead.Seal(nonce, nonce, plaintext, envelopeAAD(pairingID, messageID))
	ctB64 := b64.EncodeToString(ct)
	sig := ed25519.Sign(hubPriv, envelopeSigned(pairingID, messageID, ctB64))
	return Envelope{V: 1, P: pairingID, M: messageID, CT: ctB64, S: b64.EncodeToString(sig)}, nil
}

// OpenMessage decrypts the envelope and verifies the hub signature (what the
// device NSE does). ErrDecrypt for ciphertext/AAD problems, ErrBadSignature
// for signature problems.
func OpenMessage(hubPub ed25519.PublicKey, pairingKey []byte, e Envelope) ([]byte, error) {
	if e.V != 1 {
		return nil, ErrDecrypt
	}
	ct, err := b64.DecodeString(e.CT)
	if err != nil || len(ct) < nonceLen+16 {
		return nil, ErrDecrypt
	}
	ek, err := MessageKey(pairingKey)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(ek)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, ct[:nonceLen], ct[nonceLen:], envelopeAAD(e.P, e.M))
	if err != nil {
		return nil, ErrDecrypt
	}
	sig, err := b64.DecodeString(e.S)
	if err != nil || len(hubPub) != ed25519.PublicKeySize ||
		!ed25519.Verify(hubPub, envelopeSigned(e.P, e.M, e.CT), sig) {
		return nil, ErrBadSignature
	}
	return pt, nil
}

// WrapAtRest encrypts plaintext under the 32-byte master key: nonce(12) || AES-256-GCM.
func WrapAtRest(master, plaintext []byte) ([]byte, error) {
	return wrapAtRest(rand.Reader, master, plaintext)
}

func wrapAtRest(r io.Reader, master, plaintext []byte) ([]byte, error) {
	aead, err := newGCM(master)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, []byte(brand.Label("v1/at-rest"))), nil
}

// UnwrapAtRest reverses WrapAtRest.
func UnwrapAtRest(master, blob []byte) ([]byte, error) {
	aead, err := newGCM(master)
	if err != nil {
		return nil, err
	}
	if len(blob) < nonceLen+16 {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, blob[:nonceLen], blob[nonceLen:], []byte(brand.Label("v1/at-rest")))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
