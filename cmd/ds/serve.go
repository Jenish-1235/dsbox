package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/dockui"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/ui"
	"github.com/Jenish-1235/dsbox/internal/web"
)

// ServeAddr is where ds serve listens; Caddy forwards http://devbox and
// http://dbs.devbox here.
const ServeAddr = devproj.PagesAddr

// serve runs the devbox pages until ctx ends, or until ds itself is replaced
// by an update (it exits so systemd restarts it on the new binary).
func serve(ctx context.Context, l layout.Layout) error {
	d := devproj.Dev{S: devproj.LoadSettings(l), Out: ui.Plain(os.Stderr), W: os.Stderr}
	srv := &http.Server{
		Addr:              ServeAddr,
		Handler:           web.Server{Dev: d, Docker: dockui.NewClient(dockui.SocketPath())},
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	serveErr := make(chan error, 1)
	wg.Go(func() { serveErr <- srv.ListenAndServe() })
	wg.Go(func() { exitWhenReplaced(ctx, cancel) })

	select {
	case err := <-serveErr:
		cancel()
		wg.Wait()
		return fmt.Errorf("serve devbox pages on %s: %w", ServeAddr, err)
	case <-ctx.Done():
	}
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	err := srv.Shutdown(shutdownCtx)
	wg.Wait()
	if err != nil {
		return fmt.Errorf("stop devbox pages: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve devbox pages: %w", err)
	}
	return nil
}

// exitWhenReplaced cancels when the ds binary on disk changes (ds update).
func exitWhenReplaced(ctx context.Context, cancel context.CancelFunc) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	first, err := os.Stat(exe)
	if err != nil {
		return
	}
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if now, err := os.Stat(exe); err == nil && !os.SameFile(first, now) {
				cancel()
				return
			}
		}
	}
}
