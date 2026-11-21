package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != ed25519.PrivateKeySize {
		t.Fatalf("len %d", len(k1))
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Equal(k2) {
		t.Fatal("second call returned a different key")
	}
}

func TestLoadOrCreateKeyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.key")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("garbage key file accepted")
	}
}

func TestFingerprintRFC8032(t *testing.T) {
	// RFC 8032 section 7.1, TEST 1.
	seed, _ := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	wantPub := "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	if hex.EncodeToString(pub) != wantPub {
		t.Fatalf("test vector key mismatch")
	}
	raw, _ := hex.DecodeString(wantPub)
	sum := sha256.Sum256(raw)
	if got := Fingerprint(pub); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint %s", got)
	}
}
