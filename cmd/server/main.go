package main

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/api"
	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("konfigurasjonsfeil", "err", err)
		os.Exit(1)
	}

	// Datamappe gjelder kun SQLite-fil — en Postgres-URL har ingen katalog.
	if !strings.Contains(cfg.DBPath, "://") {
		if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
			log.Error("kunne ikke lage datamappe", "err", err)
			os.Exit(1)
		}
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("kunne ikke åpne database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := api.NewServer(cfg, log, st)
	log.Info("nordavind backend lytter", "port", cfg.Port)
	// BIND_ADDR lar prod binde til loopback — backend skal kun nås via Caddy.
	addr := os.Getenv("BIND_ADDR")
	if addr == "" {
		addr = ":" + cfg.Port
	}
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Error("server stoppet", "err", err)
		os.Exit(1)
	}
}
