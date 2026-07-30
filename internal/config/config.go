package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

// Config holder all oppstartskonfigurasjon, lest fra miljøvariabler.
type Config struct {
	Port            string
	UpstreamBaseURL string // OpenAI-kompatibelt endepunkt (Scaleway Generative APIs)
	PublicBaseURL   string // utadvendt base-URL (live Excel-lenker, OAuth-callbacks)
	MSClientID      string // Azure app-registrering (Microsoft 365-connector)
	MSClientSecret  string
	UpstreamAPIKey  string
	MistralAPIKey   string   // MISTRAL_API_KEY: Large 3 hos La Plateforme (EU)
	AllowedOrigins  []string // CORS-origins for frontend (kommaseparert i env)
	DBPath          string   // SQLite-fil for tenants/brukere/sesjoner
	AuthRequired    bool     // krev innlogging på chat/extract (default på; sett AUTH_REQUIRED=false kun i dev)
	AnswerStyle     string   // husets svarstil i chat: "off" (default) | "on"
	BrainMode       string   // hjernen (påstandsgrafen): "off" (default) | "on"
	IntentMode      string   // intent-motoren: "off" (default) | "shadow" (rut + logg, endrer ingenting)
	Engine          string   // assistentmotoren: "" (default, dagens løype) | "v6" (metodemotoren)
	SearxURL        string   // SEARXNG_URL: self-hostet søkeinstans; tom = DuckDuckGo-fallback (lokal dev)
	SerperKey       string   // SERPER_API_KEY: Googles resultater via API; tom = av. Omgår IP-blokkeringen

	// MIDLERTIDIG: hardkodet e-postkonto til /mail (env). Flyttes til
	// Connector-siden på produksjonsnivå senere.
	Mail MailConfig
}

// MailConfig er én e-postkonto lest fra env (dev).
type MailConfig struct {
	Email     string
	IMAPHost  string
	IMAPPort  int
	SMTPHost  string
	SMTPPort  int
	Password  string
	Signature string
	// Owner er den ENESTE brukeren som får bruke den delte dev-postkassen.
	// Tom verdi = mail-funksjonen er av (unngår kryss-tenant-lekkasje).
	Owner string
}

func Load() (Config, error) {
	loadDotEnv(".env")
	cfg := Config{
		Port:            getenv("PORT", "8080"),
		PublicBaseURL:   getenv("PUBLIC_BASE_URL", "http://localhost:8080"),
		MSClientID:      getenv("MS_CLIENT_ID", ""),
		MSClientSecret:  getenv("MS_CLIENT_SECRET", ""),
		UpstreamBaseURL: getenv("UPSTREAM_BASE_URL", "https://api.scaleway.ai/v1"),
		UpstreamAPIKey:  os.Getenv("UPSTREAM_API_KEY"),
		MistralAPIKey:   os.Getenv("MISTRAL_API_KEY"),
		AllowedOrigins:  strings.Split(getenv("ALLOWED_ORIGIN", "http://localhost:5173,http://127.0.0.1:5173"), ","),
		DBPath:          getenv("DB_PATH", "data/nordavind.db"),
		AuthRequired:    getenv("AUTH_REQUIRED", "true") != "false",
		AnswerStyle:     getenv("ANSWER_STYLE", "on"),
		BrainMode:       getenv("BRAIN", "off"),
		IntentMode:      getenv("INTENT_ENGINE", "off"),
		Engine:          getenv("ENGINE", ""),
		SearxURL:        getenv("SEARXNG_URL", ""),
		SerperKey:       getenv("SERPER_API_KEY", ""),
		Mail: MailConfig{
			Email:     os.Getenv("MAIL_EMAIL"),
			IMAPHost:  os.Getenv("MAIL_IMAP_HOST"),
			IMAPPort:  atoiDefault(os.Getenv("MAIL_IMAP_PORT"), 993),
			SMTPHost:  os.Getenv("MAIL_SMTP_HOST"),
			SMTPPort:  atoiDefault(os.Getenv("MAIL_SMTP_PORT"), 587),
			Password:  os.Getenv("MAIL_PASSWORD"),
			Signature: os.Getenv("MAIL_SIGNATURE"),
			Owner:     strings.ToLower(strings.TrimSpace(os.Getenv("MAIL_OWNER_EMAIL"))),
		},
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
