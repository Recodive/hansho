package hansho

import "context"

// CreatePairingTicket creates a single-use pairing ticket for a user of your
// system (valid for 10 minutes). Show PairingTicket.QR to the user as a QR
// code; the Hansho app redeems it.
func (c *Client) CreatePairingTicket(ctx context.Context, externalUserID string) (PairingTicket, error) {
	var out PairingTicket
	if externalUserID == "" {
		return out, ErrEmptyID
	}
	r, err := jsonRequest("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": externalUserID})
	if err != nil {
		return out, err
	}
	err = c.call(ctx, r, &out)
	return out, err
}

// Unpair revokes every pairing of the user.
func (c *Client) Unpair(ctx context.Context, externalUserID string) error {
	if externalUserID == "" {
		return ErrEmptyID
	}
	r, _ := jsonRequest("DELETE", "/api/v1/users/"+seg(externalUserID)+"/pairings", nil)
	return c.call(ctx, r, nil)
}
