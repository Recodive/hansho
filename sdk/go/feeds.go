package hansho

import "context"

// PutFeed sets the data behind a widget and asks the devices to reload.
func (c *Client) PutFeed(ctx context.Context, id string, f Feed) (FeedResult, error) {
	var out FeedResult
	if id == "" {
		return out, ErrEmptyID
	}
	r, err := jsonRequest("PUT", "/api/v1/feeds/"+seg(id), f)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, r, &out)
	return out, err
}
