// Command hub is the Hansho hub: the self-hosted service external backends
// use to pair users and send notifications, pages, Live Activities and widget
// feeds, delivered end-to-end encrypted through the relay.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/recodive/hansho/hub/internal/api"
	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/delivery"
	"github.com/recodive/hansho/hub/internal/identity"
	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

const usage = `Hansho Hub: the self-hosted server your backend talks to.

usage: hub <command>

commands:
  serve                 run the hub (HTTP API, delivery worker, webhooks)
  apikey create <name>  create a sender API key; the key is printed once
  relay register        register this hub with the relay; prints HUB_ID and HUB_CHALLENGE_TOKEN
  relay verify          ask the relay to verify the domain challenge

configuration is read from HUB_* environment variables (see README.md)
`

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run executes a command and returns the process exit code: 0 success,
// 1 runtime/config failure, 2 usage error.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	usageErr := func() int {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if len(args) == 0 {
		return usageErr()
	}
	switch {
	case args[0] == "serve" && len(args) == 1:
		return withConfig(getenv, stderr, func(cfg config.Config) error {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			ln, err := net.Listen("tcp", cfg.Addr)
			if err != nil {
				return err
			}
			return serve(ctx, cfg, ln, stdout)
		})
	case args[0] == "apikey" && len(args) == 3 && args[1] == "create":
		return withConfig(getenv, stderr, func(cfg config.Config) error {
			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			defer st.Close()
			key, err := st.CreateAPIKey(args[2])
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, key)
			return nil
		})
	case args[0] == "relay" && len(args) == 2 && args[1] == "register":
		return withConfig(getenv, stderr, func(cfg config.Config) error { return relayRegister(cfg, stdout) })
	case args[0] == "relay" && len(args) == 2 && args[1] == "verify":
		return withConfig(getenv, stderr, func(cfg config.Config) error {
			if cfg.HubID == "" {
				return errors.New("HUB_ID is required (run `hub relay register` first)")
			}
			c, err := newRelayClient(cfg)
			if err != nil {
				return err
			}
			if err := c.Verify(context.Background()); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "verified")
			return nil
		})
	}
	return usageErr()
}

func withConfig(getenv func(string) string, stderr io.Writer, f func(config.Config) error) int {
	cfg, err := config.Load(getenv)
	if err == nil {
		err = f(cfg)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func newRelayClient(cfg config.Config) (*relay.Client, error) {
	key, err := identity.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	return relay.New(cfg.RelayURL, cfg.HubID, key, nil), nil
}

func relayRegister(cfg config.Config, stdout io.Writer) error {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return err
	}
	c, err := newRelayClient(cfg)
	if err != nil {
		return err
	}
	key, err := identity.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	hubID, challenge, err := c.Register(context.Background(), u.Hostname(), key.Public().(ed25519.PublicKey))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "HUB_ID=%s\nHUB_CHALLENGE_TOKEN=%s\n", hubID, challenge)
	return nil
}

// serve runs the HTTP API plus the delivery and webhook loops on ln until ctx
// is cancelled, then drains in-flight requests.
func serve(ctx context.Context, cfg config.Config, ln net.Listener, stdout io.Writer) error {
	// Apps confirm a hub's verified domain through the relay using this ID, and the relay
	// rejects pushes without it, so a hub that was never registered is unusable. Fail before
	// creating any state.
	if cfg.HubID == "" {
		return errors.New("HUB_ID is not set: register this hub with the relay first (run `hub relay register`), then start it with the printed HUB_ID and HUB_CHALLENGE_TOKEN")
	}
	key, err := identity.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	rc := relay.New(cfg.RelayURL, cfg.HubID, key, nil)
	wh := webhook.New(st, cfg.WebhookURL, cfg.WebhookSecret, nil, time.Now)
	handler := api.New(api.Deps{Cfg: cfg, Store: st, Key: key, Relay: rc, Webhooks: wh, Now: time.Now})
	worker := delivery.NewWorker(st, rc, wh, cfg, key, time.Now)

	if cfg.WebhookURL != "" && len(cfg.WebhookSecret) == 0 {
		slog.Warn("HUB_WEBHOOK_SECRET is not set: webhooks are sent unsigned")
	}
	fmt.Fprintf(stdout, "hansho hub listening on %s fingerprint=%s\n", ln.Addr(), identity.Fingerprint(key.Public().(ed25519.PublicKey)))

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	loopCtx, stopLoops := context.WithCancel(ctx)
	defer stopLoops()
	loops := make(chan struct{}, 2)
	go func() { worker.Run(loopCtx, time.Second); loops <- struct{}{} }()
	go func() { runWebhooks(loopCtx, wh, time.Second); loops <- struct{}{} }()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && serveErr == nil {
		serveErr = err
	}
	stopLoops()
	<-loops
	<-loops
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return serveErr
}

func runWebhooks(ctx context.Context, wh *webhook.Dispatcher, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := wh.RunOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Error("webhook run failed", "err", err)
			}
		}
	}
}
