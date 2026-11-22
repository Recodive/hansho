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

// Kick ends the user's whole pairing, on every device. It is Unpair under its sender-side name: the app tells the
// person that the sender ended it (a 401 pairing_revoked with reason "kicked"), where an unpair they did themselves
// carries no reason. There is no block list; a new ticket pairs them again.
func (c *Client) Kick(ctx context.Context, externalUserID string) error {
	return c.Unpair(ctx, externalUserID)
}

// Unpair revokes every pairing of the user.
func (c *Client) Unpair(ctx context.Context, externalUserID string) error {
	if externalUserID == "" {
		return ErrEmptyID
	}
	r, _ := jsonRequest("DELETE", "/api/v1/users/"+seg(externalUserID)+"/pairings", nil)
	return c.call(ctx, r, nil)
}
