package crypto

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/recodive/hansho/hub/internal/brand"
)

// CanonicalString is the string signed in every authenticated request, byte-identical
// to the relay's: ts "\n" method "\n" path "\n" hex(sha256(body)).
func CanonicalString(ts, method, path string, body []byte) string {
	sum := sha256.Sum256(body)
	return ts + "\n" + method + "\n" + path + "\n" + hex.EncodeToString(sum[:])
}

// SignHubRequest returns the relay auth headers for a request.
func SignHubRequest(priv ed25519.PrivateKey, hubID string, ts time.Time, method, path string, body []byte) http.Header {
	unix := strconv.FormatInt(ts.Unix(), 10)
	sig := ed25519.Sign(priv, []byte(CanonicalString(unix, method, path, body)))
	h := http.Header{}
	h.Set(brand.Header("Hub"), hubID)
	h.Set(brand.Header("Timestamp"), unix)
	h.Set(brand.Header("Signature"), b64.EncodeToString(sig))
	return h
}

// VerifyDeviceSignature checks an ECDSA P-256/SHA-256 signature in raw r||s form
// (64 bytes, base64url) over the canonical string. DER signatures are rejected.
func VerifyDeviceSignature(pub *ecdsa.PublicKey, ts, method, path string, body []byte, sigB64 string) bool {
	if pub == nil {
		return false
	}
	sig, err := b64.DecodeString(sigB64)
	if err != nil || len(sig) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(CanonicalString(ts, method, path, body)))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	return ecdsa.Verify(pub, digest[:], r, s)
}

// DeviceAuthKey derives the key used for new-device pairing proofs.
func DeviceAuthKey(pairingKey []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, pairingKey, nil, brand.Label("v1/device-auth"), 32)
}

// PairingProof is base64url(HMAC-SHA256(HKDF(pairingKey, "…/v1/device-auth"), canonical)).
// It is empty only if key derivation fails, which cannot happen for these parameters.
func PairingProof(pairingKey []byte, canonical string) string {
	k, err := DeviceAuthKey(pairingKey)
	if err != nil {
		return ""
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(canonical))
	return b64.EncodeToString(m.Sum(nil))
}

// VerifyPairingProof compares a proof in constant time.
func VerifyPairingProof(pairingKey []byte, canonical, proof string) bool {
	want := PairingProof(pairingKey, canonical)
	return want != "" && hmac.Equal([]byte(want), []byte(proof))
}
