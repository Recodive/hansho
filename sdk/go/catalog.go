package hansho

import "context"

// ListChannels returns every channel.
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	var out []Channel
	r, _ := jsonRequest("GET", "/api/v1/channels", nil)
	err := c.call(ctx, r, &out)
	return out, err
}

// ListTemplates returns every template at its latest version.
func (c *Client) ListTemplates(ctx context.Context) ([]Template, error) {
	var out []Template
	r, _ := jsonRequest("GET", "/api/v1/templates", nil)
	err := c.call(ctx, r, &out)
	return out, err
}

// GetTemplate returns a template; a missing one is an *APIError with Code "unknown_template".
func (c *Client) GetTemplate(ctx context.Context, id string) (Template, error) {
	var out Template
	if id == "" {
		return out, ErrEmptyID
	}
	r, _ := jsonRequest("GET", "/api/v1/templates/"+seg(id), nil)
	err := c.call(ctx, r, &out)
	return out, err
}

// DeleteTemplate deletes a template. One a live channel still names is refused with an *APIError, Code "in_use".
func (c *Client) DeleteTemplate(ctx context.Context, id string) error {
	if id == "" {
		return ErrEmptyID
	}
	r, _ := jsonRequest("DELETE", "/api/v1/templates/"+seg(id), nil)
	return c.call(ctx, r, nil)
}

// ListSounds returns every sound's metadata.
func (c *Client) ListSounds(ctx context.Context) ([]Sound, error) {
	var out []Sound
	r, _ := jsonRequest("GET", "/api/v1/sounds", nil)
	err := c.call(ctx, r, &out)
	return out, err
}

// GetSound returns a sound's metadata; a missing one is an *APIError with Code "unknown_sound".
func (c *Client) GetSound(ctx context.Context, id string) (Sound, error) {
	var out Sound
	if id == "" {
		return out, ErrEmptyID
	}
	r, _ := jsonRequest("GET", "/api/v1/sounds/"+seg(id), nil)
	err := c.call(ctx, r, &out)
	return out, err
}

// DeleteSound deletes a sound. One a live channel still names is refused with an *APIError, Code "in_use".
func (c *Client) DeleteSound(ctx context.Context, id string) error {
	if id == "" {
		return ErrEmptyID
	}
	r, _ := jsonRequest("DELETE", "/api/v1/sounds/"+seg(id), nil)
	return c.call(ctx, r, nil)
}

// ListGroups returns every group.
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	var out []Group
	r, _ := jsonRequest("GET", "/api/v1/groups", nil)
	err := c.call(ctx, r, &out)
	return out, err
}
