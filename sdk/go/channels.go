package hansho

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// PutChannel creates or replaces a channel. Ids match [A-Za-z0-9][A-Za-z0-9._-]{0,63}.
func (c *Client) PutChannel(ctx context.Context, id string, ch Channel) error {
	if id == "" {
		return ErrEmptyID
	}
	r, err := jsonRequest("PUT", "/api/v1/channels/"+seg(id), ch)
	if err != nil {
		return err
	}
	return c.call(ctx, r, nil)
}

// GetChannel returns a channel; a missing or deleted channel is an *APIError
// with Code "unknown_channel".
func (c *Client) GetChannel(ctx context.Context, id string) (Channel, error) {
	var out Channel
	if id == "" {
		return out, ErrEmptyID
	}
	r, _ := jsonRequest("GET", "/api/v1/channels/"+seg(id), nil)
	err := c.call(ctx, r, &out)
	return out, err
}

// DeleteChannel deletes a channel.
func (c *Client) DeleteChannel(ctx context.Context, id string) error {
	if id == "" {
		return ErrEmptyID
	}
	r, _ := jsonRequest("DELETE", "/api/v1/channels/"+seg(id), nil)
	return c.call(ctx, r, nil)
}

// PutTemplate stores a template version; the version must increase. body is the
// template tree as JSON.
func (c *Client) PutTemplate(ctx context.Context, id string, version int, body json.RawMessage) error {
	if id == "" {
		return ErrEmptyID
	}
	r, err := jsonRequest("PUT", "/api/v1/templates/"+seg(id), templateRequest{Version: version, Body: body})
	if err != nil {
		return err
	}
	return c.call(ctx, r, nil)
}

const maxSoundBytes = 1 << 20

// PutSound uploads a notification sound (audio/x-caf, audio/wav or audio/aiff,
// at most 1 MB).
func (c *Client) PutSound(ctx context.Context, id, contentType string, rd io.Reader) (SoundResult, error) {
	var out SoundResult
	if id == "" {
		return out, ErrEmptyID
	}
	data, err := io.ReadAll(io.LimitReader(rd, maxSoundBytes+1))
	if err != nil {
		return out, err
	}
	if len(data) > maxSoundBytes {
		return out, ErrSoundTooLarge
	}
	r := request{method: "PUT", path: "/api/v1/sounds/" + seg(id), body: bytes.Clone(data), contentType: contentType, retry: true}
	err = c.call(ctx, r, &out)
	return out, err
}
