// Command hollowtone-server is the personal cloud server for HollowTone. It
// stores your music library metadata and audio files and keeps multiple
// devices in sync over a small HTTP/JSON API.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luolingy/HollowTone/server/internal/api"
	"github.com/luolingy/HollowTone/server/internal/config"
	"github.com/luolingy/HollowTone/server/internal/storage"
	"github.com/luolingy/HollowTone/server/internal/store"
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

	srv := api.New(cfg, st, stor)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No global WriteTimeout: large audio streams may take a while.
		IdleTimeout: 120 * time.Second,
	}

	go func() {
		log.Printf("listening on %s (data=%s)", cfg.Addr, cfg.DataDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("bye")
}
