package hansho

import "context"

type pairingOptions struct{ displayName *string }

// PairingOption configures CreatePairingTicket.
type PairingOption func(*pairingOptions)

// WithDisplayName sets what other people see for the user instead of their id (any language, trimmed, at most 40
// characters, no control characters).
func WithDisplayName(name string) PairingOption {
	return func(o *pairingOptions) { o.displayName = &name }
}

// CreatePairingTicket creates a single-use pairing ticket for a user of your
// system (valid for 10 minutes). Show PairingTicket.QR to the user as a QR
// code; the Hansho app redeems it.
// Pass WithDisplayName to give the user a display name with the ticket.
func (c *Client) CreatePairingTicket(ctx context.Context, externalUserID string, opts ...PairingOption) (PairingTicket, error) {
	var out PairingTicket
	if externalUserID == "" {
		return out, ErrEmptyID
	}
	var po pairingOptions
	for _, o := range opts {
		o(&po)
	}
	body := map[string]string{"external_user_id": externalUserID}
	if po.displayName != nil {
		body["display_name"] = *po.displayName
	}
	r, err := jsonRequest("POST", "/api/v1/pairing-tickets", body)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, r, &out)
	return out, err
}

// SetUserDisplayName sets what other people see for the user instead of their external user id: any language,
// trimmed, at most 40 characters, no control characters. "" clears it. The user need not be paired yet.
func (c *Client) SetUserDisplayName(ctx context.Context, externalUserID, displayName string) error {
	if externalUserID == "" {
		return ErrEmptyID
	}
	r, err := jsonRequest("PUT", "/api/v1/users/"+seg(externalUserID), map[string]string{"display_name": displayName})
	if err != nil {
		return err
	}
	return c.call(ctx, r, nil)
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
