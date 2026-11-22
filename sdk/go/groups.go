package hansho

import "context"

// PutGroup creates a group or replaces its whole member list (external user ids, who may not have paired yet).
// Audiences that name the group follow at once. Ids match [A-Za-z0-9][A-Za-z0-9_-]{0,63}.
func (c *Client) PutGroup(ctx context.Context, id string, members []string) (Group, error) {
	var out Group
	if id == "" {
		return out, ErrEmptyID
	}
	if members == nil {
		members = []string{} // the hub requires the list, even an empty one
	}
	r, err := jsonRequest("PUT", "/api/v1/groups/"+seg(id), map[string][]string{"members": members})
	if err != nil {
		return out, err
	}
	err = c.call(ctx, r, &out)
	return out, err
}

// GetGroup returns a group; a missing one is an *APIError with Code CodeUnknownGroup.
func (c *Client) GetGroup(ctx context.Context, id string) (Group, error) {
	var out Group
	if id == "" {
		return out, ErrEmptyID
	}
	r, _ := jsonRequest("GET", "/api/v1/groups/"+seg(id), nil)
	err := c.call(ctx, r, &out)
	return out, err
}

// DeleteGroup deletes a group. An audience that still names it matches nobody through it.
func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	if id == "" {
		return ErrEmptyID
	}
	r, _ := jsonRequest("DELETE", "/api/v1/groups/"+seg(id), nil)
	return c.call(ctx, r, nil)
}
