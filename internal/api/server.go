package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

// Server ruter OpenAI-kompatible forespørsler videre til upstream-endepunktet.
type Server struct {
	cfg    config.Config
	client *http.Client
	log    *slog.Logger
}

func NewServer(cfg config.Config, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg,
		// Ingen total timeout: streaming-svar kan stå åpne lenge.
		client: &http.Client{Timeout: 0},
		log:    log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
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
	body, pickedModel := withRoutingDefaults(body)

	upstreamURL := strings.TrimSuffix(s.cfg.UpstreamBaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.UpstreamAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.UpstreamAPIKey)
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
func withRoutingDefaults(body []byte) ([]byte, string) {
	var payload struct {
		Model    string           `json:"model"`
		Messages []router.Message `json:"messages"`
	}
	var full map[string]any
	if err := json.Unmarshal(body, &full); err != nil {
		return body, ""
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, ""
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
				"content": "Svar alltid så kort som mulig: færrest mulig setninger og ord. " +
					"Hard grense: maks 5 setninger totalt. Ingen innledning, ingen oppsummering, " +
					"ingen gjentakelse av spørsmålet. Bruk lister kun når det er strengt nødvendig.",
			}
			full["messages"] = append([]any{system}, raw...)
		}
	}

	model := payload.Model
	if model == "auto" {
		model = router.Pick(payload.Messages)
		full["model"] = model
		if model == router.HeavyModel {
			// Tyngre spørsmål får resonnering uansett hva klienten ba om.
			full["reasoning"] = map[string]any{"enabled": true}
		}
	}
	if _, ok := full["provider"]; !ok {
		full["provider"] = map[string]any{"sort": "throughput"}
	}

	patched, err := json.Marshal(full)
	if err != nil {
		return body, model
	}
	return patched, model
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
