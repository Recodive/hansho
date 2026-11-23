// Package client is a small client for the Hansho hub's sender API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponse = 4 << 20

// Client talks to one hub with one API key. The key is only ever sent as a bearer token and is never put in errors.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New checks the endpoint and returns a client. The endpoint must be https; plain http is accepted only for a loopback
// host, so the API key cannot be sent in the clear by mistake.
func New(endpoint, apiKey string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("endpoint must be an https URL such as https://hub.example.com")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("endpoint must use https (http is only accepted for localhost)")
	}
	if apiKey == "" {
		return nil, errors.New("api_key must not be empty")
	}
	return &Client{base: strings.TrimRight(u.String(), "/"), key: apiKey, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// APIError is an error answer from the hub: {"error","field","message"}.
type APIError struct {
	Status  int
	Code    string
	Field   string
	Message string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("hub answered %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Field != "" {
		s += " (" + e.Field + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// IsNotFound reports whether err is a 404 from the hub.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// do sends one request, with body (if any) as given. A non-nil out receives the JSON answer.
func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL has nothing the caller lacks; keep the message short
		}
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("%s %s: reading answer: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		e := &APIError{Status: resp.StatusCode}
		var b struct{ Error, Field, Message string }
		if json.Unmarshal(data, &b) == nil {
			e.Code, e.Field, e.Message = b.Error, b.Field, b.Message
		}
		return e
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: unexpected answer: %w", method, path, err)
		}
	}
	return nil
}

func (c *Client) putJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, path, "application/json", b, out)
}

// Action is one button of a channel.
type Action struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind,omitempty"`
	URL   string `json:"url,omitempty"`
	Ack   *bool  `json:"ack,omitempty"`
}

// Audience says who may use a channel.
type Audience struct {
	Mode   string   `json:"mode"`
	Users  []string `json:"users"`
	Groups []string `json:"groups"`
}

// Channel is a channel as the hub takes and returns it. ID is in the path, not the PUT body.
type Channel struct {
	ID           string   `json:"id,omitempty"`
	Name         string   `json:"name"`
	DefaultLevel string   `json:"default_level"`
	MaxLevel     string   `json:"max_level"`
	Sound        string   `json:"sound"`
	TemplateID   string   `json:"template_id"`
	Thread       string   `json:"thread"`
	Actions      []Action `json:"actions"`
	Audience     Audience `json:"audience"`
}

func (c *Client) PutChannel(ctx context.Context, id string, ch Channel) (Channel, error) {
	var out Channel
	return out, c.putJSON(ctx, "/api/v1/channels/"+url.PathEscape(id), ch, &out)
}

func (c *Client) GetChannel(ctx context.Context, id string) (Channel, error) {
	var out Channel
	return out, c.do(ctx, http.MethodGet, "/api/v1/channels/"+url.PathEscape(id), "", nil, &out)
}

func (c *Client) DeleteChannel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/channels/"+url.PathEscape(id), "", nil, nil)
}

// PutGroup replaces the member list and returns the stored one.
func (c *Client) PutGroup(ctx context.Context, id string, members []string) ([]string, error) {
	if members == nil {
		members = []string{}
	}
	var out struct{ Members []string }
	err := c.putJSON(ctx, "/api/v1/groups/"+url.PathEscape(id), map[string]any{"members": members}, &out)
	return out.Members, err
}

func (c *Client) GetGroup(ctx context.Context, id string) ([]string, error) {
	var out struct{ Members []string }
	err := c.do(ctx, http.MethodGet, "/api/v1/groups/"+url.PathEscape(id), "", nil, &out)
	return out.Members, err
}

func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/groups/"+url.PathEscape(id), "", nil, nil)
}

// Template is a stored template. The hub wants each PUT to carry a higher version than the last.
type Template struct {
	ID      string          `json:"id"`
	Version int             `json:"version"`
	Body    json.RawMessage `json:"body"`
}

func (c *Client) PutTemplate(ctx context.Context, id string, version int, body json.RawMessage) error {
	return c.putJSON(ctx, "/api/v1/templates/"+url.PathEscape(id), Template{Version: version, Body: body}, nil)
}

func (c *Client) GetTemplate(ctx context.Context, id string) (Template, error) {
	var out Template
	return out, c.do(ctx, http.MethodGet, "/api/v1/templates/"+url.PathEscape(id), "", nil, &out)
}

func (c *Client) DeleteTemplate(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/templates/"+url.PathEscape(id), "", nil, nil)
}

// Sound is a sound's metadata; the hub never returns the audio on the sender API.
type Sound struct {
	ID          string `json:"id"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
}

// PutSound uploads raw audio and returns the hub's sha256 and size of it.
func (c *Client) PutSound(ctx context.Context, id, contentType string, audio []byte) (sha256 string, size int, err error) {
	var out struct {
		SHA256 string
		Size   int
	}
	err = c.do(ctx, http.MethodPut, "/api/v1/sounds/"+url.PathEscape(id), contentType, audio, &out)
	return out.SHA256, out.Size, err
}

func (c *Client) GetSound(ctx context.Context, id string) (Sound, error) {
	var out Sound
	return out, c.do(ctx, http.MethodGet, "/api/v1/sounds/"+url.PathEscape(id), "", nil, &out)
}

func (c *Client) DeleteSound(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/sounds/"+url.PathEscape(id), "", nil, nil)
}
