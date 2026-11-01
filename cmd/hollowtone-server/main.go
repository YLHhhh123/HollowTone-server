// Command hollowtone-server is the personal cloud server for HollowTone. It
// stores your music library metadata and audio files and keeps multiple
// devices in sync over a small HTTP/JSON API.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/YLHhhh123/HollowTone-server/internal/api"
	"github.com/YLHhhh123/HollowTone-server/internal/config"
	"github.com/YLHhhh123/HollowTone-server/internal/storage"
	"github.com/YLHhhh123/HollowTone-server/internal/store"
	"github.com/YLHhhh123/HollowTone-server/internal/version"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[hollowtone] ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	stor, err := storage.New(cfg.MediaDir, cfg.ArtDir, cfg.ChunkUploadDir())
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	// Cancelled on SIGINT/SIGTERM; also stops the upload janitor.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Abandoned resumable uploads would otherwise pin disk space forever.
	stor.StartJanitor(ctx, cfg.JanitorInterval, cfg.UploadTTL)

	srv := api.New(cfg, st, stor)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No global ReadTimeout/WriteTimeout: uploads and large audio streams
		// are legitimately long-lived; they are bounded by body limits instead.
		IdleTimeout: 120 * time.Second,
	}

	info := version.Current()
	log.Printf("hollowtone-server %s (commit %s, built %s, %s, %s)",
		info.Version, info.Commit, info.BuildTime, info.GoVersion, info.Platform)
	if info.VCSModified {
		log.Printf("warning: built from a modified working tree; version identity is approximate")
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s (data=%s)", cfg.Addr, cfg.DataDir)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	case <-ctx.Done():
	}

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("bye")
}
