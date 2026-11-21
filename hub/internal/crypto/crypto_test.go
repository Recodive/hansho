package crypto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strconv"
	"testing"
	"time"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func x963(t testing.TB, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	b, err := pub.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newKA(t testing.TB) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newHubKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestWrapUnwrapRoundTrip(t *testing.T) {
	ka := newKA(t)
	key := bytes.Repeat([]byte{7}, 32)
	w, err := WrapKey(ka.PublicKey(), "pair_1", key)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 65+12+32+16 {
		t.Fatalf("wrapped length %d", len(w))
	}
	if w[0] != 0x04 {
		t.Fatal("ephemeral key not uncompressed")
	}
	got, err := UnwrapKey(ka, "pair_1", w)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("unwrap: %v %x", err, got)
	}
	if _, err := UnwrapKey(ka, "pair_2", w); err == nil {
		t.Fatal("unwrap with other pairing id (AAD/salt) succeeded")
	}
	if _, err := UnwrapKey(newKA(t), "pair_1", w); err == nil {
		t.Fatal("unwrap with other device key succeeded")
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	hub := newHubKey(t)
	pk := bytes.Repeat([]byte{9}, 32)
	e, err := SealMessage(hub, pk, "pair_1", "msg_1", []byte(`{"title":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.V != 1 || e.P != "pair_1" || e.M != "msg_1" {
		t.Fatalf("%+v", e)
	}
	pt, err := OpenMessage(hub.Public().(ed25519.PublicKey), pk, e)
	if err != nil || string(pt) != `{"title":"hi"}` {
		t.Fatalf("%v %q", err, pt)
	}
	j, _ := json.Marshal(e)
	var m map[string]any
	_ = json.Unmarshal(j, &m)
	for _, k := range []string{"v", "p", "m", "ct", "s"} {
		if _, ok := m[k]; !ok {
			t.Errorf("json key %q missing", k)
		}
	}
}

func flipB64(t *testing.T, s string) string {
	t.Helper()
	b, err := b64.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 1
	return b64.EncodeToString(b)
}

func TestEnvelopeTamper(t *testing.T) {
	hub := newHubKey(t)
	pub := hub.Public().(ed25519.PublicKey)
	pk := bytes.Repeat([]byte{9}, 32)
	e, _ := SealMessage(hub, pk, "pair_1", "msg_1", []byte("secret"))

	bad := e
	bad.CT = flipB64(t, e.CT)
	if _, err := OpenMessage(pub, pk, bad); err != ErrDecrypt {
		t.Errorf("tampered ct: want ErrDecrypt, got %v", err)
	}

	bad = e
	bad.S = flipB64(t, e.S)
	if _, err := OpenMessage(pub, pk, bad); err != ErrBadSignature {
		t.Errorf("tampered s: want ErrBadSignature, got %v", err)
	}

	otherPub := newHubKey(t).Public().(ed25519.PublicKey)
	if _, err := OpenMessage(otherPub, pk, e); err != ErrBadSignature {
		t.Errorf("wrong hub key: want ErrBadSignature, got %v", err)
	}

	// Swapped message id: the AAD binds it, so decryption fails even though
	// the attacker could keep the ciphertext intact.
	bad = e
	bad.M = "msg_2"
	if _, err := OpenMessage(pub, pk, bad); err == nil {
		t.Error("swapped m accepted")
	}
	bad = e
	bad.P = "pair_2"
	if _, err := OpenMessage(pub, pk, bad); err == nil {
		t.Error("swapped p accepted")
	}

	// Re-signed (by the hub) ciphertext that was tampered: decrypt must fail.
	bad = e
	bad.CT = flipB64(t, e.CT)
	bad.S = b64.EncodeToString(ed25519.Sign(hub, envelopeSigned(bad.P, bad.M, bad.CT)))
	if _, err := OpenMessage(pub, pk, bad); err != ErrDecrypt {
		t.Errorf("re-signed tampered ct: want ErrDecrypt, got %v", err)
	}

	// Wrong pairing key.
	if _, err := OpenMessage(pub, bytes.Repeat([]byte{1}, 32), e); err != ErrDecrypt {
		t.Errorf("wrong pairing key: got %v", err)
	}
}

// relayVerify is a hand-written copy of the relay's hubauth verification
// (relay/internal/hubauth), independent of this package's implementation.
func relayVerify(pub ed25519.PublicKey, hubID string, h map[string][]string, method, path string, body []byte) bool {
	get := func(k string) string {
		if v := h[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	ts := get("X-Hansho-Timestamp")
	sig, err := b64.DecodeString(get("X-Hansho-Signature"))
	if err != nil || get("X-Hansho-Hub") != hubID {
		return false
	}
	if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
		return false
	}
	sum := sha256.Sum256(body)
	msg := ts + "\n" + method + "\n" + path + "\n" + hex.EncodeToString(sum[:])
	return ed25519.Verify(pub, []byte(msg), sig)
}

func TestSignHubRequestMatchesRelayVerifier(t *testing.T) {
	priv := newHubKey(t)
	body := []byte(`{"sealed":"x"}`)
	ts := time.Unix(1_700_000_000, 0)
	h := SignHubRequest(priv, "hub_abc", ts, "POST", "/v1/push", body)
	if !relayVerify(priv.Public().(ed25519.PublicKey), "hub_abc", h, "POST", "/v1/push", body) {
		t.Fatal("relay-style verifier rejected signature")
	}
	if relayVerify(priv.Public().(ed25519.PublicKey), "hub_abc", h, "POST", "/v1/push", []byte("other")) {
		t.Fatal("verifier accepted other body")
	}
	if h.Get("X-Hansho-Timestamp") != "1700000000" {
		t.Fatalf("timestamp header %q", h.Get("X-Hansho-Timestamp"))
	}
	if got := CanonicalString("1700000000", "POST", "/v1/push", body); got !=
		"1700000000\nPOST\n/v1/push\n"+hex.EncodeToString(func() []byte { s := sha256.Sum256(body); return s[:] }()) {
		t.Fatalf("canonical %q", got)
	}
}

func rawSig(t testing.TB, priv *ecdsa.PrivateKey, canonical string) []byte {
	t.Helper()
	d := sha256.Sum256([]byte(canonical))
	r, s, err := ecdsa.Sign(rand.Reader, priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return out
}

func TestVerifyDeviceSignature(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"a":1}`)
	canon := CanonicalString("1700000000", "POST", "/device/v1/actions", body)
	sig := rawSig(t, priv, canon)
	if !VerifyDeviceSignature(&priv.PublicKey, "1700000000", "POST", "/device/v1/actions", body, b64.EncodeToString(sig)) {
		t.Fatal("valid raw signature rejected")
	}
	if VerifyDeviceSignature(&priv.PublicKey, "1700000001", "POST", "/device/v1/actions", body, b64.EncodeToString(sig)) {
		t.Fatal("wrong timestamp accepted")
	}
	if VerifyDeviceSignature(&priv.PublicKey, "1700000000", "POST", "/device/v1/actions", []byte("x"), b64.EncodeToString(sig)) {
		t.Fatal("wrong body accepted")
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if VerifyDeviceSignature(&other.PublicKey, "1700000000", "POST", "/device/v1/actions", body, b64.EncodeToString(sig)) {
		t.Fatal("wrong key accepted")
	}
	// DER encoding of the same signature must be rejected.
	der, _ := asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])})
	if VerifyDeviceSignature(&priv.PublicKey, "1700000000", "POST", "/device/v1/actions", body, b64.EncodeToString(der)) {
		t.Fatal("DER signature accepted")
	}
	if VerifyDeviceSignature(&priv.PublicKey, "1700000000", "POST", "/device/v1/actions", body, "!!") {
		t.Fatal("garbage accepted")
	}
}

func TestPairingProof(t *testing.T) {
	pk := bytes.Repeat([]byte{3}, 32)
	p := PairingProof(pk, "canon")
	if p == "" || p != PairingProof(pk, "canon") {
		t.Fatal("proof not deterministic")
	}
	if p == PairingProof(pk, "canon2") || p == PairingProof(bytes.Repeat([]byte{4}, 32), "canon") {
		t.Fatal("proof does not depend on inputs")
	}
	if !VerifyPairingProof(pk, "canon", p) || VerifyPairingProof(pk, "canon", p+"x") {
		t.Fatal("VerifyPairingProof")
	}
}

func TestAtRest(t *testing.T) {
	master := bytes.Repeat([]byte{5}, 32)
	b, err := WrapAtRest(master, []byte("pairing key"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("pairing key")) {
		t.Fatal("plaintext visible in blob")
	}
	got, err := UnwrapAtRest(master, b)
	if err != nil || string(got) != "pairing key" {
		t.Fatal(err)
	}
	if _, err := UnwrapAtRest(bytes.Repeat([]byte{6}, 32), b); err != ErrDecrypt {
		t.Fatalf("wrong master: %v", err)
	}
	if _, err := UnwrapAtRest(master, b[:5]); err != ErrDecrypt {
		t.Fatalf("short blob: %v", err)
	}
}

func TestActivityStateRoundTrip(t *testing.T) {
	pk := bytes.Repeat([]byte{8}, 32)
	blob, err := SealActivityState(pk, []byte(`{"t":"x","d":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenActivityState(pk, blob)
	if err != nil || string(got) != `{"t":"x","d":{}}` {
		t.Fatalf("%v %q", err, got)
	}
	blob[len(blob)-1] ^= 1
	if _, err := OpenActivityState(pk, blob); err != ErrDecrypt {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := OpenActivityState(bytes.Repeat([]byte{9}, 32), blob); err != ErrDecrypt {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := OpenActivityState(pk, []byte{1, 2}); err != ErrDecrypt {
		t.Fatalf("short: %v", err)
	}
}

// ---- test vectors ----

type vectors struct {
	Hub struct {
		SeedHex     string `json:"seed_hex"`
		PublicHex   string `json:"public_hex"`
		Fingerprint string `json:"fingerprint"`
	} `json:"hub"`
	Wrap struct {
		DeviceKAPrivHex string `json:"device_ka_priv_hex"`
		DeviceKAPubHex  string `json:"device_ka_pub_hex"`
		PairingID       string `json:"pairing_id"`
		PairingKeyHex   string `json:"pairing_key_hex"`
		EphPrivHex      string `json:"eph_priv_hex"`
		NonceHex        string `json:"nonce_hex"`
		WrapKeyHex      string `json:"wrap_key_hex"`
		WrappedB64      string `json:"wrapped_b64url"`
	} `json:"wrap"`
	Envelope struct {
		PairingID     string   `json:"pairing_id"`
		MessageID     string   `json:"message_id"`
		Plaintext     string   `json:"plaintext"`
		NonceHex      string   `json:"nonce_hex"`
		MessageKeyHex string   `json:"message_key_hex"`
		Envelope      Envelope `json:"envelope"`
	} `json:"envelope"`
	Request struct {
		HubID     string `json:"hub_id"`
		Timestamp string `json:"timestamp"`
		Method    string `json:"method"`
		Path      string `json:"path"`
		Body      string `json:"body"`
		Canonical string `json:"canonical"`
		Signature string `json:"signature_b64url"`
	} `json:"hub_request"`
	Device struct {
		PrivHex    string `json:"priv_hex"`
		PubX963Hex string `json:"pub_x963_hex"`
		Timestamp  string `json:"timestamp"`
		Method     string `json:"method"`
		Path       string `json:"path"`
		Body       string `json:"body"`
		Signature  string `json:"signature_b64url"`
	} `json:"device_signature"`
	Proof struct {
		PairingKeyHex    string `json:"pairing_key_hex"`
		Canonical        string `json:"canonical"`
		DeviceAuthKeyHex string `json:"device_auth_key_hex"`
		Proof            string `json:"proof_b64url"`
	} `json:"pairing_proof"`
	Activity struct {
		PairingKeyHex string `json:"pairing_key_hex"`
		KeyHex        string `json:"activity_key_hex"`
		NonceHex      string `json:"nonce_hex"`
		Plaintext     string `json:"plaintext"`
		BlobB64       string `json:"blob_b64url"`
	} `json:"activity_state"`
	AtRest struct {
		MasterHex string `json:"master_hex"`
		NonceHex  string `json:"nonce_hex"`
		Plaintext string `json:"plaintext"`
		BlobB64   string `json:"blob_b64url"`
	} `json:"at_rest"`
}

// fixedBytes returns n deterministic bytes derived from label (test inputs only).
func fixedBytes(label string, n int) []byte {
	out := make([]byte, 0, n+32)
	for i := 0; len(out) < n; i++ {
		s := sha256.Sum256([]byte(label + strconv.Itoa(i)))
		out = append(out, s[:]...)
	}
	return out[:n]
}

func hubPrivFromVectors(t testing.TB, v *vectors) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(mustHex(t, v.Hub.SeedHex))
}

func deviceKeyFromVectors(t testing.TB, v *vectors) *ecdsa.PrivateKey {
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), mustHex(t, v.Device.PrivHex))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// buildVectors computes every output from fixed inputs. Only used with
// HANSHO_GEN_VECTORS=1 (see TestGenerateVectors). The ECDSA signature is the
// one non-deterministic primitive; it is generated once and then verified.
func buildVectors(t *testing.T) *vectors {
	v := &vectors{}
	// Hub key.
	seed := fixedBytes("hub-seed", 32)
	hub := ed25519.NewKeyFromSeed(seed)
	v.Hub.SeedHex = hex.EncodeToString(seed)
	v.Hub.PublicHex = hex.EncodeToString(hub.Public().(ed25519.PublicKey))
	fp := sha256.Sum256(hub.Public().(ed25519.PublicKey))
	v.Hub.Fingerprint = hex.EncodeToString(fp[:])

	// Key wrap.
	ka, err := ecdh.P256().NewPrivateKey(fixedBytes("device-ka", 32))
	if err != nil {
		t.Fatal(err)
	}
	v.Wrap.DeviceKAPrivHex = hex.EncodeToString(ka.Bytes())
	v.Wrap.DeviceKAPubHex = hex.EncodeToString(ka.PublicKey().Bytes())
	v.Wrap.PairingID = "pair_vector_1"
	pk := fixedBytes("pairing-key", 32)
	v.Wrap.PairingKeyHex = hex.EncodeToString(pk)
	eph := fixedBytes("wrap-eph", 32)
	nonce := fixedBytes("wrap-nonce", 12)
	v.Wrap.EphPrivHex = hex.EncodeToString(eph)
	v.Wrap.NonceHex = hex.EncodeToString(nonce)
	wrapped, err := wrapKey(bytes.NewReader(append(append([]byte{}, eph...), nonce...)), ka.PublicKey(), v.Wrap.PairingID, pk)
	if err != nil {
		t.Fatal(err)
	}
	v.Wrap.WrappedB64 = b64.EncodeToString(wrapped)
	ephKey, _ := ecdh.P256().NewPrivateKey(eph)
	shared, _ := ephKey.ECDH(ka.PublicKey())
	wk, _ := wrapKDF(shared, v.Wrap.PairingID)
	v.Wrap.WrapKeyHex = hex.EncodeToString(wk)

	// Envelope.
	v.Envelope.PairingID = v.Wrap.PairingID
	v.Envelope.MessageID = "msg_vector_1"
	v.Envelope.Plaintext = `{"title":"Disk full","body":"db-1 is at 97%","channel":"ops","level":"time-sensitive"}`
	n2 := fixedBytes("env-nonce", 12)
	v.Envelope.NonceHex = hex.EncodeToString(n2)
	ek, _ := MessageKey(pk)
	v.Envelope.MessageKeyHex = hex.EncodeToString(ek)
	env, err := sealMessage(bytes.NewReader(n2), hub, pk, v.Envelope.PairingID, v.Envelope.MessageID, []byte(v.Envelope.Plaintext))
	if err != nil {
		t.Fatal(err)
	}
	v.Envelope.Envelope = env

	// Hub request signature.
	v.Request.HubID = "hub_vector"
	v.Request.Timestamp = "1700000000"
	v.Request.Method = "POST"
	v.Request.Path = "/v1/push"
	v.Request.Body = `{"sealed":"AAAA","kind":"alert","priority":10,"payload":{"aps":{}}}`
	v.Request.Canonical = CanonicalString(v.Request.Timestamp, v.Request.Method, v.Request.Path, []byte(v.Request.Body))
	h := SignHubRequest(hub, v.Request.HubID, time.Unix(1700000000, 0), v.Request.Method, v.Request.Path, []byte(v.Request.Body))
	v.Request.Signature = h.Get("X-Hansho-Signature")

	// Device signature.
	dpriv := fixedBytes("device-sign", 32)
	v.Device.PrivHex = hex.EncodeToString(dpriv)
	dk, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), dpriv)
	if err != nil {
		t.Fatal(err)
	}
	v.Device.PubX963Hex = hex.EncodeToString(x963(t, &dk.PublicKey))
	v.Device.Timestamp = "1700000000"
	v.Device.Method = "POST"
	v.Device.Path = "/device/v1/actions"
	v.Device.Body = `{"message_id":"msg_vector_1","action_id":"ack"}`
	v.Device.Signature = b64.EncodeToString(rawSig(t, dk, CanonicalString(v.Device.Timestamp, v.Device.Method, v.Device.Path, []byte(v.Device.Body))))

	// Pairing proof.
	v.Proof.PairingKeyHex = v.Wrap.PairingKeyHex
	v.Proof.Canonical = CanonicalString("1700000000", "POST", "/device/v1/pairings/pair_vector_1/devices", []byte(`{}`))
	dak, _ := DeviceAuthKey(pk)
	v.Proof.DeviceAuthKeyHex = hex.EncodeToString(dak)
	v.Proof.Proof = PairingProof(pk, v.Proof.Canonical)

	// Live Activity content-state.
	v.Activity.PairingKeyHex = v.Wrap.PairingKeyHex
	ak, _ := ActivityKey(pk)
	v.Activity.KeyHex = hex.EncodeToString(ak)
	n4 := fixedBytes("activity-nonce", 12)
	v.Activity.NonceHex = hex.EncodeToString(n4)
	v.Activity.Plaintext = `{"t":"deploy","d":{"progress":0.4}}`
	ablob, err := sealActivityState(bytes.NewReader(n4), pk, []byte(v.Activity.Plaintext))
	if err != nil {
		t.Fatal(err)
	}
	v.Activity.BlobB64 = b64.EncodeToString(ablob)

	// At rest.
	master := fixedBytes("master", 32)
	n3 := fixedBytes("rest-nonce", 12)
	v.AtRest.MasterHex = hex.EncodeToString(master)
	v.AtRest.NonceHex = hex.EncodeToString(n3)
	v.AtRest.Plaintext = "pairing-key-bytes"
	blob, err := wrapAtRest(bytes.NewReader(n3), master, []byte(v.AtRest.Plaintext))
	if err != nil {
		t.Fatal(err)
	}
	v.AtRest.BlobB64 = b64.EncodeToString(blob)
	return v
}

// TestGenerateVectors rewrites testdata/vectors.json. It is a test (not a
// `go run` file) because the deterministic primitives are unexported.
//
//	HANSHO_GEN_VECTORS=1 go test ./internal/crypto -run TestGenerateVectors
func TestGenerateVectors(t *testing.T) {
	if os.Getenv("HANSHO_GEN_VECTORS") != "1" {
		t.Skip("set HANSHO_GEN_VECTORS=1 to regenerate testdata/vectors.json")
	}
	b, err := json.MarshalIndent(buildVectors(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/vectors.json", append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVectors regenerates nothing: it checks every primitive against the
// checked-in file, which the Swift app also verifies.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}

	// Hub identity.
	hub := hubPrivFromVectors(t, &v)
	pub := hub.Public().(ed25519.PublicKey)
	if hex.EncodeToString(pub) != v.Hub.PublicHex {
		t.Fatal("hub public key")
	}
	if fp := sha256.Sum256(pub); hex.EncodeToString(fp[:]) != v.Hub.Fingerprint {
		t.Fatal("hub fingerprint")
	}

	// Wrap: the device unwraps the vector, and the deterministic wrap reproduces it byte for byte.
	ka, err := ecdh.P256().NewPrivateKey(mustHex(t, v.Wrap.DeviceKAPrivHex))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(ka.PublicKey().Bytes()) != v.Wrap.DeviceKAPubHex {
		t.Fatal("device KA public key")
	}
	wrapped, err := b64.DecodeString(v.Wrap.WrappedB64)
	if err != nil {
		t.Fatal(err)
	}
	pk := mustHex(t, v.Wrap.PairingKeyHex)
	got, err := UnwrapKey(ka, v.Wrap.PairingID, wrapped)
	if err != nil || !bytes.Equal(got, pk) {
		t.Fatalf("unwrap vector: %v", err)
	}
	rnd := append(mustHex(t, v.Wrap.EphPrivHex), mustHex(t, v.Wrap.NonceHex)...)
	again, err := wrapKey(bytes.NewReader(rnd), ka.PublicKey(), v.Wrap.PairingID, pk)
	if err != nil || !bytes.Equal(again, wrapped) {
		t.Fatalf("wrap not reproducible: %v", err)
	}
	ephKey, _ := ecdh.P256().NewPrivateKey(mustHex(t, v.Wrap.EphPrivHex))
	shared, _ := ephKey.ECDH(ka.PublicKey())
	if wk, _ := wrapKDF(shared, v.Wrap.PairingID); hex.EncodeToString(wk) != v.Wrap.WrapKeyHex {
		t.Fatal("wrap HKDF output")
	}

	// Envelope.
	if ek, _ := MessageKey(pk); hex.EncodeToString(ek) != v.Envelope.MessageKeyHex {
		t.Fatal("message key")
	}
	pt, err := OpenMessage(pub, pk, v.Envelope.Envelope)
	if err != nil || string(pt) != v.Envelope.Plaintext {
		t.Fatalf("open vector envelope: %v", err)
	}
	e2, err := sealMessage(bytes.NewReader(mustHex(t, v.Envelope.NonceHex)), hub, pk, v.Envelope.PairingID, v.Envelope.MessageID, []byte(v.Envelope.Plaintext))
	if err != nil || e2 != v.Envelope.Envelope {
		t.Fatalf("seal not reproducible: %v", err)
	}

	// Hub request signing.
	body := []byte(v.Request.Body)
	if c := CanonicalString(v.Request.Timestamp, v.Request.Method, v.Request.Path, body); c != v.Request.Canonical {
		t.Fatalf("canonical %q", c)
	}
	ts, _ := strconv.ParseInt(v.Request.Timestamp, 10, 64)
	h := SignHubRequest(hub, v.Request.HubID, time.Unix(ts, 0), v.Request.Method, v.Request.Path, body)
	if h.Get("X-Hansho-Signature") != v.Request.Signature || h.Get("X-Hansho-Hub") != v.Request.HubID {
		t.Fatal("hub request signature")
	}

	// Device signature (ECDSA is randomised; verify the stored one).
	dk := deviceKeyFromVectors(t, &v)
	if hex.EncodeToString(x963(t, &dk.PublicKey)) != v.Device.PubX963Hex {
		t.Fatal("device sign public key")
	}
	if !VerifyDeviceSignature(&dk.PublicKey, v.Device.Timestamp, v.Device.Method, v.Device.Path, []byte(v.Device.Body), v.Device.Signature) {
		t.Fatal("device signature vector")
	}

	// Pairing proof.
	if k, _ := DeviceAuthKey(pk); hex.EncodeToString(k) != v.Proof.DeviceAuthKeyHex {
		t.Fatal("device-auth key")
	}
	if PairingProof(pk, v.Proof.Canonical) != v.Proof.Proof {
		t.Fatal("pairing proof")
	}

	// Live Activity content-state.
	if k, _ := ActivityKey(pk); hex.EncodeToString(k) != v.Activity.KeyHex {
		t.Fatal("activity key")
	}
	ablob, _ := b64.DecodeString(v.Activity.BlobB64)
	if p, err := OpenActivityState(pk, ablob); err != nil || string(p) != v.Activity.Plaintext {
		t.Fatalf("activity vector: %v", err)
	}
	a2, err := sealActivityState(bytes.NewReader(mustHex(t, v.Activity.NonceHex)), pk, []byte(v.Activity.Plaintext))
	if err != nil || !bytes.Equal(a2, ablob) {
		t.Fatal("activity state not reproducible")
	}

	// At rest.
	master := mustHex(t, v.AtRest.MasterHex)
	blob, _ := b64.DecodeString(v.AtRest.BlobB64)
	if p, err := UnwrapAtRest(master, blob); err != nil || string(p) != v.AtRest.Plaintext {
		t.Fatalf("at-rest vector: %v", err)
	}
	b2, err := wrapAtRest(bytes.NewReader(mustHex(t, v.AtRest.NonceHex)), master, []byte(v.AtRest.Plaintext))
	if err != nil || !bytes.Equal(b2, blob) {
		t.Fatal("at-rest not reproducible")
	}
}
