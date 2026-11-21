// Package crypto implements the hub's wire cryptography: pairing-key wrap
// (ECIES), the signed message envelope, request signing and at-rest wrapping.
// The formats are normative for the Swift app; testdata/vectors.json pins them.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/recodive/hansho/hub/internal/brand"
)

const (
	p256PubLen = 65 // uncompressed x9.63
	nonceLen   = 12
)

var (
	// ErrDecrypt means AES-GCM authentication or decryption failed.
	ErrDecrypt = errors.New("crypto: decrypt failed")
	// ErrBadSignature means the hub signature did not verify.
	ErrBadSignature = errors.New("crypto: bad signature")
)

func newGCM(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// ephemeralKey draws a P-256 scalar from r (crypto/rand in production; a fixed
// stream when generating test vectors).
func ephemeralKey(r io.Reader) (*ecdh.PrivateKey, error) {
	for range 8 {
		var b [32]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		if k, err := ecdh.P256().NewPrivateKey(b[:]); err == nil {
			return k, nil
		}
	}
	return nil, errors.New("crypto: could not draw ephemeral key")
}

func wrapKDF(shared []byte, pairingID string) ([]byte, error) {
	return hkdf.Key(sha256.New, shared, []byte(pairingID), brand.Label("v1/wrap"), 32)
}

// WrapKey encrypts key to the device key-agreement public key (ECIES):
// eph_pub(65) || nonce(12) || AESGCM(k, key, aad=pairingID).
func WrapKey(devKA *ecdh.PublicKey, pairingID string, key []byte) ([]byte, error) {
	return wrapKey(rand.Reader, devKA, pairingID, key)
}

func wrapKey(r io.Reader, devKA *ecdh.PublicKey, pairingID string, key []byte) ([]byte, error) {
	if devKA == nil || devKA.Curve() != ecdh.P256() {
		return nil, errors.New("crypto: device key must be P-256")
	}
	eph, err := ephemeralKey(r)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(devKA)
	if err != nil {
		return nil, err
	}
	k, err := wrapKDF(shared, pairingID)
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
	out := append([]byte{}, eph.PublicKey().Bytes()...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, key, []byte(pairingID)), nil
}

// UnwrapKey reverses WrapKey. It exists for tests and vectors; the device does this.
func UnwrapKey(devKA *ecdh.PrivateKey, pairingID string, wrapped []byte) ([]byte, error) {
	if len(wrapped) < p256PubLen+nonceLen+16 {
		return nil, fmt.Errorf("crypto: wrapped key too short: %w", ErrDecrypt)
	}
	ephPub, err := ecdh.P256().NewPublicKey(wrapped[:p256PubLen])
	if err != nil {
		return nil, fmt.Errorf("crypto: bad ephemeral key: %w", ErrDecrypt)
	}
	shared, err := devKA.ECDH(ephPub)
	if err != nil {
		return nil, ErrDecrypt
	}
	k, err := wrapKDF(shared, pairingID)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	nonce := wrapped[p256PubLen : p256PubLen+nonceLen]
	pt, err := aead.Open(nil, nonce, wrapped[p256PubLen+nonceLen:], []byte(pairingID))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
