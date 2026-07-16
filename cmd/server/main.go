package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/jacobgjorva/backend-nordavind/internal/api"
	"github.com/jacobgjorva/backend-nordavind/internal/config"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("konfigurasjonsfeil", "err", err)
		os.Exit(1)
	}

	srv := api.NewServer(cfg, log)
	log.Info("nordavind backend lytter", "port", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, srv.Handler()); err != nil {
		log.Error("server stoppet", "err", err)
		os.Exit(1)
	}
}
