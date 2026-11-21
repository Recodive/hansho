package crypto

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"io"

	"github.com/recodive/hansho/hub/internal/brand"
)

// ActivityKey derives the Live Activity content-state key from a pairing key.
func ActivityKey(pairingKey []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, pairingKey, nil, brand.Label("v1/activity"), 32)
}

// SealActivityState encrypts a Live Activity content-state: nonce(12) || ciphertext || tag(16).
func SealActivityState(pairingKey, plaintext []byte) ([]byte, error) {
	return sealActivityState(rand.Reader, pairingKey, plaintext)
}

func sealActivityState(r io.Reader, pairingKey, plaintext []byte) ([]byte, error) {
	k, err := ActivityKey(pairingKey)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// OpenActivityState reverses SealActivityState (what the widget does).
func OpenActivityState(pairingKey, blob []byte) ([]byte, error) {
	k, err := ActivityKey(pairingKey)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	if len(blob) < nonceLen+16 {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, blob[:nonceLen], blob[nonceLen:], nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
