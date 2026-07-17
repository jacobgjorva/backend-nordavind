package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/search"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Server ruter OpenAI-kompatible forespørsler videre til upstream-endepunktet.
type Server struct {
	cfg     config.Config
	client  *http.Client
	log     *slog.Logger
	search  *search.Client
	store   *store.Store
	pricing *pricing
	rates   *usdNok
}

func NewServer(cfg config.Config, log *slog.Logger, st *store.Store) *Server {
	s := &Server{
		cfg: cfg,
		// Ingen total timeout: streaming-svar kan stå åpne lenge.
		client:  &http.Client{Timeout: 0},
		log:     log,
		search:  search.NewClient(),
		store:   st,
		pricing: newPricing(),
		rates:   &usdNok{},
	}
	go s.pricing.refreshLoop(&http.Client{Timeout: 30 * time.Second}, cfg.UpstreamBaseURL, cfg.UpstreamAPIKey)
	return s
}

// newUpstreamRequest lager en autentisert POST mot upstream chat/completions.
func (s *Server) newUpstreamRequest(ctx context.Context, body io.Reader) (*http.Request, error) {
	upstreamURL := strings.TrimSuffix(s.cfg.UpstreamBaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.UpstreamAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.UpstreamAPIKey)
	}
	return req, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/auth/request-code", s.handleRequestCode)
	mux.HandleFunc("POST /v1/auth/verify", s.handleVerifyCode)
	mux.HandleFunc("GET /v1/auth/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("GET /v1/usage/daily", s.requireAuth(s.handleUsageDaily))
	mux.HandleFunc("GET /v1/chats", s.requireAuth(s.handleListChats))
	mux.HandleFunc("POST /v1/chats", s.requireAuth(s.handleCreateChat))
	mux.HandleFunc("GET /v1/chats/{id}", s.requireAuth(s.handleGetChat))
	mux.HandleFunc("POST /v1/chats/{id}/messages", s.requireAuth(s.handleAppendChatMessage))
	mux.HandleFunc("DELETE /v1/chats/{id}", s.requireAuth(s.handleDeleteChat))
	mux.HandleFunc("POST /v1/chats/{id}/title", s.requireAuth(s.handleGenerateChatTitle))
	mux.HandleFunc("GET /v1/admin/users", s.requireAdmin(s.handleAdminListUsers))
	mux.HandleFunc("POST /v1/admin/users", s.requireAdmin(s.handleAdminCreateUser))
	mux.HandleFunc("DELETE /v1/admin/users/{id}", s.requireAdmin(s.handleAdminDeleteUser))
	mux.HandleFunc("POST /v1/chat/completions", s.requireAuth(s.handleChatCompletions))
	mux.HandleFunc("POST /v1/extract", s.requireAuth(s.handleExtract))
	return s.cors(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "kunne ikke lese request", http.StatusBadRequest)
		return
	}
	patched, full, pickedModel := withRoutingDefaults(body)

	// Streaming-forespørsler kjøres i agent-løkken (web_search som verktøy).
	if wantsStream, _ := full["stream"].(bool); wantsStream {
		s.runAgentLoop(r.Context(), w, full)
		s.log.Info("chat/completions",
			"mode", "agent",
			"model", pickedModel,
			"dur", time.Since(start).Round(time.Millisecond),
		)
		return
	}

	req, err := s.newUpstreamRequest(r.Context(), bytes.NewReader(patched))
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Error("upstream-kall feilet", "err", err)
		http.Error(w, "upstream utilgjengelig", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	flushCopy(w, resp.Body)

	s.log.Info("chat/completions",
		"status", resp.StatusCode,
		"model", pickedModel,
		"dur", time.Since(start).Round(time.Millisecond),
	)
}

// withRoutingDefaults gjør to ting: løser "auto" til konkret modell ut fra
// spørsmålets kompleksitet, og sorterer leverandører på throughput hvis
// klienten ikke har bedt om noe annet — enkelte leverandører har flere
// sekunder høyere time-to-first-token for samme modell.
func withRoutingDefaults(body []byte) ([]byte, map[string]any, string) {
	var payload struct {
		Model    string           `json:"model"`
		Messages []router.Message `json:"messages"`
	}
	var full map[string]any
	if err := json.Unmarshal(body, &full); err != nil {
		return body, map[string]any{}, ""
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, full, ""
	}

	// Hard stil-grense: korte svar, maks 5 setninger. Legges først hvis
	// klienten ikke selv har satt en system-melding.
	hasSystem := false
	for _, m := range payload.Messages {
		if m.Role == "system" {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		if raw, ok := full["messages"].([]any); ok {
			system := map[string]any{
				"role": "system",
				"content": "Svar kortest mulig på korrekt norsk, maks 5 setninger. Ingen innledning, " +
					"oppsummering eller gjentakelse av spørsmålet. Aldri gjett: bruk web_search for " +
					"fakta du er usikker på, og si fra hvis kildene ikke dekker svaret. Skriv aldri " +
					"URL-er eller kildehenvisninger — de vises automatisk. Ved råd: land én tydelig " +
					"anbefaling. Kun hvis forespørselen er for vag til å kunne besvares: still ett " +
					"oppklarende spørsmål.",
			}
			full["messages"] = append([]any{system}, raw...)
		}
	}

	model := router.Resolve(payload.Model)
	if model != payload.Model {
		full["model"] = model
	}
	if model == "auto" {
		model = router.Pick(payload.Messages)
		full["model"] = model
		if model == router.HeavyModel {
			// Tyngre spørsmål får resonnering uansett hva klienten ba om.
			full["reasoning"] = map[string]any{"enabled": true}
		}
	}
	if _, ok := full["provider"]; !ok {
		if model == router.MidModel {
			// Enkelte tredjeparts-leverandører (f.eks. nebul) feiler på
			// tool-streaming for Mistral — bruk Mistrals egen.
			full["provider"] = map[string]any{"order": []string{"mistral"}}
		} else {
			full["provider"] = map[string]any{"sort": "throughput"}
		}
	}

	patched, err := json.Marshal(full)
	if err != nil {
		return body, full, model
	}
	return patched, full, model
}

// flushCopy kopierer upstream-responsen til klienten og flusher fortløpende,
// slik at SSE-tokens når frontend uten buffering.
func flushCopy(w http.ResponseWriter, r io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if slices.Contains(s.cfg.AllowedOrigins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
