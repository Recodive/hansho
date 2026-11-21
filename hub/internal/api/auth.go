package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/recodive/hansho/hub/internal/brand"
	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/store"
)

const (
	// maxSkew is the accepted difference between a signed timestamp and now.
	maxSkew = 300 * time.Second
	// nonceTTL must exceed twice maxSkew so a replay can never outlive its cache entry: a timestamp is accepted for
	// 2*maxSkew, and the cache works in whole seconds, so it gets a margin on top.
	nonceTTL = 2*maxSkew + time.Minute
)

var (
	headerDevice       = brand.Header("Device")
	headerTimestamp    = brand.Header("Timestamp")
	headerSignature    = brand.Header("Signature")
	headerPairingProof = brand.Header("Pairing-Proof")
)

// withAPIKey authenticates a sender with "Authorization: Bearer <key>".
func (s *server) withAPIKey(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if _, err := s.Store.LookupAPIKey(strings.TrimSpace(tok)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
			} else {
				s.internal(w, "api key lookup failed", err)
			}
			return
		}
		h(w, r)
	}
}

// parseSignPub parses an ECDSA P-256 public key: x9.63 uncompressed (65 bytes)
// or the bare x||y form (64 bytes).
func parseSignPub(b []byte) (*ecdsa.PublicKey, error) {
	if len(b) == 64 {
		b = append([]byte{0x04}, b...)
	}
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), b)
}

// checkTimestamp parses a unix-seconds header and enforces the skew window.
func (s *server) checkTimestamp(ts string) bool {
	v, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	d := s.Now().Sub(time.Unix(v, 0))
	return d <= maxSkew && d >= -maxSkew
}

// deviceHandler receives the authenticated device and the (already read) body.
type deviceHandler func(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, body []byte)

// withDevice authenticates a request signed by a paired device: ECDSA P-256 over
// the canonical string, +-300 s skew, replay cache keyed by the signature's value (nonceTTL, see
// crypto.SignatureReplayKey).
func (s *server) withDevice(h deviceHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r, maxJSONBody)
		if !ok {
			return
		}
		id, ts, sig := r.Header.Get(headerDevice), r.Header.Get(headerTimestamp), r.Header.Get(headerSignature)
		if id == "" || ts == "" || sig == "" || !s.checkTimestamp(ts) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		dev, err := s.Store.GetDevice(id)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			s.internal(w, "device lookup failed", err)
			return
		}
		pub, err := parseSignPub(dev.SignPub)
		if err != nil || !crypto.VerifyDeviceSignature(pub, ts, r.Method, r.URL.Path, body, sig) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Only record the nonce once the signature is proven, so forged requests
		// cannot fill the cache or pre-burn a legitimate signature.
		if !s.recordNonce(w, "sig:"+crypto.SignatureReplayKey(sig)) {
			return
		}
		// Only a caller that proves it holds the device's key learns why it is refused. A retired device is not a
		// revoked pairing: the app re-registers in the first case and discards the pairing only in the second.
		switch {
		case !dev.Pairing.RevokedAt.IsZero():
			writeErr(w, http.StatusUnauthorized, "pairing_revoked")
			return
		case !dev.RevokedAt.IsZero():
			writeErr(w, http.StatusUnauthorized, "device_revoked")
			return
		}
		h(w, r, dev, body)
	}
}

// recordNonce rejects a replayed value with 409. It returns false after writing the response.
func (s *server) recordNonce(w http.ResponseWriter, nonce string) bool {
	seen, err := s.Store.SeenNonce(nonce, nonceTTL)
	if err != nil {
		s.internal(w, "nonce check failed", err)
		return false
	}
	if seen {
		writeErr(w, http.StatusConflict, "replay")
		return false
	}
	return true
}
