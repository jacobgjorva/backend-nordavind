package api

import (
	"net/http"
	"context"
	"sync"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Intent-motoren kobles foran chatten bak INTENT_ENGINE-flagget:
//   off    — motoren finnes ikke (default).
//   shadow — hver vanlige chatmelding rutes og LOGGES, men ingenting endres.
//   on     — motorens valg styrer flyten: deterministiske flyter besvares i
//            kode, rutede flyter får kun sitt verktøysett/modell, og MaxChars
//            håndheves via den eksisterende komprimerings-nødbremsen.
// Sticky (flyt over flere meldinger) er IKKE med i v1 — hver melding rutes
// på nytt; loggen viser om det trengs.

// intentState holder motoren når den er bygget. Bygging skjer i bakgrunnen
// ved oppstart (embedding av registeret) — frem til da er motoren nil og alt
// oppfører seg som før (fail-open, alltid).
type intentState struct {
	mu     sync.RWMutex
	engine *intent.Engine
}

func (st *intentState) get() *intent.Engine {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.engine
}

func (st *intentState) set(e *intent.Engine) {
	st.mu.Lock()
	st.engine = e
	st.mu.Unlock()
}

// initIntentEngine bygger motoren i bakgrunnen. Feiler embeddingen prøves det
// igjen med backoff — motoren dukker opp når den er klar, aldri i veien.
func (s *Server) initIntentEngine() {
	if s.cfg.IntentMode != "shadow" && s.cfg.IntentMode != "on" {
		return
	}
	embedder := &intent.ScalewayEmbedder{
		BaseURL: s.cfg.UpstreamBaseURL, APIKey: s.cfg.UpstreamAPIKey,
		Model: "qwen3-embedding-8b", Client: s.client,
	}
	judge := &intent.ScalewayJudge{
		BaseURL: s.cfg.UpstreamBaseURL, APIKey: s.cfg.UpstreamAPIKey,
		Model: router.MidModel, Client: s.client,
	}
	go func() {
		for attempt := 1; ; attempt++ {
			eng, err := intent.New(context.Background(), embedder, judge, s.log, nil)
			if err == nil {
				s.intent.set(eng)
				s.log.Info("intent-motor klar", "mode", s.cfg.IntentMode, "hash", eng.RegistryHash)
				return
			}
			s.log.Warn("intent-motor: bygging feilet, prøver igjen", "forsøk", attempt, "err", err)
			if attempt >= 5 {
				s.log.Error("intent-motor: gir opp — chatten kjører uten (fail-open)")
				return
			}
			time.Sleep(time.Duration(attempt) * 10 * time.Second)
		}
	}()
}

// shadowIntent ruter meldingen og logger avgjørelsen — uten å påvirke svaret.
// Kjøres i egen goroutine med eget context så den aldri koster chat-latens.
func (s *Server) shadowIntent(user store.User, message string) {
	eng := s.intent.get()
	if eng == nil || message == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d := eng.Resolve(ctx, message, user.Role == "admin")
		if err := s.store.InsertIntentDecision(store.IntentDecision{
			TenantID:   user.TenantID,
			UserID:     user.ID,
			Message:    message,
			Key:        d.Key,
			Method:     d.Method,
			Candidates: intent.MarshalCandidates(d.Candidates),
			ElapsedMS:  int(d.Elapsed.Milliseconds()),
		}); err != nil {
			s.log.Warn("intent-shadow: kunne ikke logge avgjørelse", "err", err)
		}
		s.log.Info("intent-shadow", "key", orFree(d.Key), "method", d.Method,
			"ms", d.Elapsed.Milliseconds())
	}()
}

// Interne payload-nøkler for flyt-kontrakten (settes av applyIntent, leses i
// runAgentLoop, og strippes før payloaden går upstream).
const (
	flowKeyField = "nordavind_flow"
	flowMaxField = "nordavind_flow_max"
)

// deterministicPanels: flyter som besvares HELT i kode med et admin-panel.
// Øvrige deterministiske flyter (eksport, kontrakt, opplasting) eies av
// frontend-løyper og faller til fri chat her.
var deterministicPanels = map[string]string{
	"usage_stats":        "forbruk",
	"manage_users":       "tilganger",
	"knowledge_admin":    "kunnskap",
	"employees_admin":    "ansatte",
	"manage_connections": "tilkoblinger",
}

// flowModel mapper flyt-radens modellnivå til router-konstantene.
func flowModel(level string) string {
	switch level {
	case "heavy":
		return router.HeavyModel
	case "top":
		return router.TopModel
	default:
		return router.MidModel
	}
}

// applyIntent (mode=on): ruter meldingen synkront, logger avgjørelsen og
// muterer payloaden etter flyt-kontrakten. Returnerer et ferdig blokk-svar
// for deterministiske panel-flyter ("" ellers). Fail-open hele veien: nil
// motor eller fri chat → payloaden røres ikke.
func (s *Server) applyIntent(user store.User, full map[string]any) (block string) {
	eng := s.intent.get()
	msg := lastUserText(full)
	if eng == nil || msg == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	d := eng.Resolve(ctx, msg, user.Role == "admin")
	go func() {
		if err := s.store.InsertIntentDecision(store.IntentDecision{
			TenantID: user.TenantID, UserID: user.ID, Message: msg,
			Key: d.Key, Method: d.Method,
			Candidates: intent.MarshalCandidates(d.Candidates),
			ElapsedMS:  int(d.Elapsed.Milliseconds()),
		}); err != nil {
			s.log.Warn("intent: kunne ikke logge avgjørelse", "err", err)
		}
	}()
	key, flow := intent.FlowFor(d)
	s.log.Info("intent", "key", key, "method", d.Method, "ms", d.Elapsed.Milliseconds())

	if flow.Deterministic {
		if panel, ok := deterministicPanels[key]; ok {
			return "```admin\n" + panel + "\n```"
		}
		if key == "connect_database" {
			return credentialBlock(msg)
		}
		// Deterministisk uten server-løype: fri chat (fallback).
		return ""
	}
	// Flyt-kontrakt inn i payloaden. Kun flyter med definerte verktøy snevres;
	// resten beholder dagens fulle oppsett (free_chat-kontrakten).
	full[flowKeyField] = key
	if flow.MaxChars > 0 {
		full[flowMaxField] = flow.MaxChars
	}
	full["model"] = flowModel(flow.Model)
	return ""
}

func orFree(k string) string {
	if k == "" {
		return intent.FreeChatKey
	}
	return k
}

// respondSSEBlock streamer ett ferdig innholdsblokk-svar (deterministisk flyt)
// i samme SSE-format som agent-løkka, så frontend ikke ser forskjell.
func (s *Server) respondSSEBlock(w http.ResponseWriter, block string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(contentSSE(block) + "\n\n"))
	w.Write([]byte("data: [DONE]\n\n"))
}
