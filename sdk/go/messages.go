package hansho

import "context"

// SendMessage sends a notification, or a page when m.Level is LevelPage.
//
// It is never retried automatically, because a repeated send would notify the
// user twice. Set m.IdempotencyKey to opt in to retries of 429 and 503.
func (c *Client) SendMessage(ctx context.Context, m Message) (MessageResult, error) {
	var out MessageResult
	r, err := jsonRequest("POST", "/api/v1/messages", m)
	if err != nil {
		return out, err
	}
	if m.IdempotencyKey != "" {
		r.retry = true
		r.header = map[string]string{"Idempotency-Key": m.IdempotencyKey}
	}
	err = c.call(ctx, r, &out)
	return out, err
}

// GetMessage returns a message's status and per-device deliveries.
func (c *Client) GetMessage(ctx context.Context, id string) (MessageStatus, error) {
	var out MessageStatus
	if id == "" {
		return out, ErrEmptyID
	}
	r, _ := jsonRequest("GET", "/api/v1/messages/"+seg(id), nil)
	err := c.call(ctx, r, &out)
	return out, err
}
