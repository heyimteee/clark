package imessage

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/tools"
)

// Options wires the iMessage transport to the rest of clark.
type Options struct {
	// Out persists outbound messages for the macOS bridge to deliver.
	Out OutboundStore
	// Butler and Notifier are the shared gateway pipeline dependencies.
	Butler   gateway.Butler
	Notifier gateway.Notifier
	Tools    *tools.Registry
	// SelfHandle is the Master's own iMessage handle ("+6281111111111").
	SelfHandle string
	// BypassPhrase is the urgent-alert command word (default "get him to me").
	BypassPhrase string
	// ListenAddr is where the bridge-facing HTTP API listens, e.g. ":8090".
	ListenAddr string
	// Token authenticates the bridge via the X-Clark-Bridge-Token header.
	Token string
	// NameToHandle resolves a VIP name or number to a canonical identity for
	// the send_imessage tool.
	NameToHandle func(input string) (string, bool)
}

// Run starts the bridge-facing HTTP server and blocks until ctx is done.
func Run(ctx context.Context, opts Options) error {
	if opts.ListenAddr == "" {
		opts.ListenAddr = ":8090"
	}

	msgr := NewMessenger(opts.Out, opts.SelfHandle)
	handler := gateway.NewHandler("IMESSAGE", msgr, opts.Butler, opts.Notifier, opts.BypassPhrase)
	if opts.Tools != nil && opts.NameToHandle != nil {
		registerSendMessageTool(opts.Tools, msgr, opts.NameToHandle)
	}

	httpServer := &http.Server{
		Addr:              opts.ListenAddr,
		Handler:           NewServer(opts.Token, opts.SelfHandle, opts.Out, handler).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logging.Log("IMESSAGE", logging.SevNotice, "SERVER", "Bridge server listening", "addr", opts.ListenAddr)
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		handler.Close()
		return err
	case <-ctx.Done():
		handler.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		logging.Log("IMESSAGE", logging.SevInfo, "SERVER", "Bridge server stopped")
		return nil
	}
}
