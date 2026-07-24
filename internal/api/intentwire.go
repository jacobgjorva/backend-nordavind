package api

import (
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
//            Loggen (intent_decisions) er sammenligningsgrunnlaget før påslag
//            og treningssett for en fremtidig klassifiserer.
// Full påvirkning av flyt (verktøysett, modell, MaxChars) kommer som eget
// steg når shadow-tallene er gode.

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
	if s.cfg.IntentMode != "shadow" {
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

func orFree(k string) string {
	if k == "" {
		return intent.FreeChatKey
	}
	return k
}
