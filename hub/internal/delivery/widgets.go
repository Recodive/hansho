package delivery

import (
	"context"

	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
)

// widgetReloadPayload tells the system to reload the app's widget timelines.
const widgetReloadPayload = `{"aps":{"content-changed":true}}`

// feedSyncPayload wakes the app to fetch its feeds. The widget only reads the cache the app keeps (it cannot sign
// requests), so a widgets push alone redraws the old value; the app reloads the widgets once the new data is cached.
const feedSyncPayload = `{"aps":{"content-available":1},"c":{"feeds":1}}`

// ReloadWidgets tells every given device that a feed changed: a background push so the app fetches it, and a widgets
// push so the system reloads the widgets. It returns how many devices accepted at least one.
func (w *Worker) ReloadWidgets(ctx context.Context, devices []store.DeviceWithPairing) (int, error) {
	cache := newEnvCache()
	sent := 0
	for _, dev := range devices {
		if !w.usable(dev) || cache.revoked[dev.Pairing.ID] {
			continue
		}
		ok := false
		for _, p := range []struct {
			sealed, kind, payload string
			token                 store.TokenKind
		}{
			{dev.SealedBackground, "background", feedSyncPayload, store.TokenBackground},
			{dev.SealedWidgets, "widgets", widgetReloadPayload, store.TokenWidgets},
		} {
			if p.sealed == "" || cache.revoked[dev.Pairing.ID] {
				continue
			}
			_, err := w.relay.Push(ctx, relay.PushRequest{Sealed: p.sealed, Kind: p.kind, Priority: 5, Payload: []byte(p.payload)})
			if err != nil {
				if ctx.Err() != nil {
					return sent, ctx.Err()
				}
				w.classify(ctx, err, dev, cache, func() { w.clearTokens(dev.ID, p.token) })
				continue
			}
			ok = true
		}
		if ok {
			sent++
		}
	}
	return sent, nil
}
