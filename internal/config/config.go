package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holder all oppstartskonfigurasjon, lest fra miljøvariabler.
type Config struct {
	Port            string
	UpstreamBaseURL string // OpenAI-kompatibelt endepunkt (Scaleway Generative APIs)
	UpstreamAPIKey  string
	AllowedOrigins  []string // CORS-origins for frontend (kommaseparert i env)
	DBPath          string   // SQLite-fil for tenants/brukere/sesjoner
	AuthRequired    bool     // krev innlogging på chat/extract (av i dev til frontend er klar)
}

func Load() (Config, error) {
	loadDotEnv(".env")
	cfg := Config{
		Port:            getenv("PORT", "8080"),
		UpstreamBaseURL: getenv("UPSTREAM_BASE_URL", "https://api.scaleway.ai/v1"),
		UpstreamAPIKey:  os.Getenv("UPSTREAM_API_KEY"),
		AllowedOrigins:  strings.Split(getenv("ALLOWED_ORIGIN", "http://localhost:5173,http://127.0.0.1:5173"), ","),
		DBPath:          getenv("DB_PATH", "data/nordavind.db"),
		AuthRequired:    getenv("AUTH_REQUIRED", "false") == "true",
	}
	if cfg.UpstreamAPIKey == "" {
		return cfg, fmt.Errorf("UPSTREAM_API_KEY må være satt")
	}
	return cfg, nil
}

// loadDotEnv setter variabler fra en .env-fil hvis de ikke alt er satt i miljøet.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
