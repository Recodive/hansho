package hansho

import "context"

// StartActivity starts a Live Activity on the user's devices.
func (c *Client) StartActivity(ctx context.Context, a Activity) (ActivityStarted, error) {
	var out ActivityStarted
	r, err := jsonRequest("POST", "/api/v1/activities", a)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, r, &out)
	return out, err
}

// UpdateActivity pushes new content to a running Live Activity. Updates are not
// retried automatically.
func (c *Client) UpdateActivity(ctx context.Context, id string, u ActivityUpdate) (ActivityUpdated, error) {
	var out ActivityUpdated
	if id == "" {
		return out, ErrEmptyID
	}
	r, err := jsonRequest("PATCH", "/api/v1/activities/"+seg(id), u)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, r, &out)
	return out, err
}

// EndActivity ends a Live Activity.
func (c *Client) EndActivity(ctx context.Context, id string) error {
	if id == "" {
		return ErrEmptyID
	}
	r, _ := jsonRequest("DELETE", "/api/v1/activities/"+seg(id), nil)
	return c.call(ctx, r, nil)
}
