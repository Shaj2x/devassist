// Package httpserver runs an http.Server until its context is cancelled,
// then shuts it down gracefully.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Run serves handler on addr until ctx is cancelled, then drains in-flight
// requests for up to grace before returning.
func Run(ctx context.Context, log *slog.Logger, addr string, handler http.Handler, grace time.Duration) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	return Serve(ctx, log, ln, handler, grace)
}

// Serve is Run with an existing listener (lets tests use port 0).
func Serve(ctx context.Context, log *slog.Logger, ln net.Listener, handler http.Handler, grace time.Duration) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", ln.Addr().String())
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("http server shutting down")
	// ctx is already cancelled; keep its values but not its cancellation so
	// in-flight requests get the full grace period.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
