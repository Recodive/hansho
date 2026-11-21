// Command fakedevice is a minimal stand-in for the Hansho app, for the SDK
// integration tests. It speaks just enough of the hub's device API to exist as a
// paired device: redeem a pairing ticket with freshly generated P-256 keys,
// register a Live Activity token and press an action button (acknowledging a
// page), signing every request the way the app does.
//
// It uses only the standard library: the hub's crypto package is internal to the
// hub module. The signed string is
// timestamp "\n" method "\n" path "\n" hex(sha256(body)) (see hub/README.md).
//
//	fakedevice freeport
//	fakedevice pair  -hub URL -ticket T -state FILE [-name NAME]
//	fakedevice ack   -state FILE -message ID [-action ack] [-text T]
//	fakedevice activity-token -state FILE -activity ID
package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

type state struct {
	Hub       string `json:"hub"`
	DeviceID  string `json:"device_id"`
	PairingID string `json:"pairing_id"`
	SignKey   string `json:"sign_key"` // PKCS#8, base64
}

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: fakedevice freeport|pair|ack|activity-token"))
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "freeport":
		err = freePort()
	case "pair":
		err = pair(args)
	case "ack":
		err = ack(args)
	case "activity-token":
		err = activityToken(args)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fakedevice:", err)
	os.Exit(1)
}

func freePort() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Println(ln.Addr().(*net.TCPAddr).Port)
	return nil
}

func pair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	hub := fs.String("hub", "", "hub base URL")
	ticket := fs.String("ticket", "", "pairing ticket")
	statePath := fs.String("state", "", "file to write the device state to")
	name := fs.String("name", "SDK integration phone", "device name")
	fs.Parse(args)
	if *hub == "" || *ticket == "" || *statePath == "" {
		return errors.New("pair needs -hub, -ticket and -state")
	}
	sign, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	ka, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	signPub, err := sign.PublicKey.Bytes()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"ticket": *ticket,
		"device": map[string]any{
			"sign_pub": base64.StdEncoding.EncodeToString(signPub),
			"ka_pub":   base64.StdEncoding.EncodeToString(ka.PublicKey().Bytes()),
			"platform": "ios", "name": *name,
		},
		"tokens": map[string]string{
			"alert": "sdk-alert-token", "background": "sdk-background-token",
			"pushtostart": "sdk-pushtostart-token", "widgets": "sdk-widgets-token",
		},
	})
	resp, err := do(*hub, "POST", "/device/v1/pairings", body, nil)
	if err != nil {
		return err
	}
	var out struct {
		PairingID string `json:"pairing_id"`
		DeviceID  string `json:"device_id"`
	}
	if err := json.Unmarshal(resp, &out); err != nil || out.DeviceID == "" {
		return fmt.Errorf("unexpected pairing response: %s", resp)
	}
	der, err := x509.MarshalPKCS8PrivateKey(sign)
	if err != nil {
		return err
	}
	st, _ := json.Marshal(state{Hub: *hub, DeviceID: out.DeviceID, PairingID: out.PairingID, SignKey: base64.StdEncoding.EncodeToString(der)})
	if err := os.WriteFile(*statePath, st, 0o600); err != nil {
		return err
	}
	fmt.Println(string(resp))
	return nil
}

func ack(args []string) error {
	fs := flag.NewFlagSet("ack", flag.ExitOnError)
	statePath := fs.String("state", "", "device state file")
	msg := fs.String("message", "", "message id")
	action := fs.String("action", "ack", "action id")
	text := fs.String("text", "", "reply text")
	fs.Parse(args)
	st, key, err := load(*statePath)
	if err != nil {
		return err
	}
	payload := map[string]string{"message_id": *msg, "action_id": *action}
	if *text != "" {
		payload["text"] = *text
	}
	body, _ := json.Marshal(payload)
	_, err = signed(st, key, "POST", "/device/v1/actions", body)
	return err
}

func activityToken(args []string) error {
	fs := flag.NewFlagSet("activity-token", flag.ExitOnError)
	statePath := fs.String("state", "", "device state file")
	act := fs.String("activity", "", "activity id")
	fs.Parse(args)
	st, key, err := load(*statePath)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"sealed_update": "sdk-activity-update-token"})
	_, err = signed(st, key, "POST", "/device/v1/activities/"+*act+"/token", body)
	return err
}

func load(path string) (state, *ecdsa.PrivateKey, error) {
	var st state
	b, err := os.ReadFile(path)
	if err != nil {
		return st, nil, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, nil, err
	}
	der, err := base64.StdEncoding.DecodeString(st.SignKey)
	if err != nil {
		return st, nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return st, nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return st, nil, errors.New("state holds a non-ECDSA key")
	}
	return st, ec, nil
}

// signed sends a device-authenticated request (ECDSA P-256, raw r||s, base64url).
func signed(st state, key *ecdsa.PrivateKey, method, path string, body []byte) ([]byte, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sum := sha256.Sum256(body)
	canonical := ts + "\n" + method + "\n" + path + "\n" + hex.EncodeToString(sum[:])
	digest := sha256.Sum256([]byte(canonical))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return do(st.Hub, method, path, body, map[string]string{
		"X-Hansho-Device":    st.DeviceID,
		"X-Hansho-Timestamp": ts,
		"X-Hansho-Signature": base64.RawURLEncoding.EncodeToString(sig),
	})
}

func do(hub, method, path string, body []byte, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(method, hub+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	return b, nil
}
