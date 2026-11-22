package hansho

import (
	"context"
	"net/url"
)

// GetFeed returns a feed's settings without its data. Leave externalUserID empty for the feed shared by all users; a
// missing feed is an *APIError with Code "unknown_feed".
func (c *Client) GetFeed(ctx context.Context, id, externalUserID string) (FeedSettings, error) {
	var out FeedSettings
	if id == "" {
		return out, ErrEmptyID
	}
	path := "/api/v1/feeds/" + seg(id)
	if externalUserID != "" {
		path += "?external_user_id=" + url.QueryEscape(externalUserID)
	}
	r, _ := jsonRequest("GET", path, nil)
	err := c.call(ctx, r, &out)
	return out, err
}

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
