package api

import (
	"context"
	"net/http"
	"strings"
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
// Sticky: korte oppfølginger uten klart eget intent-treff arver forrige
// brukermeldings flyt når den er merket Sticky (se applyIntent).

// intentState holder motoren når den er bygget. Bygging skjer i bakgrunnen
// ved oppstart (embedding av registeret) — frem til da er motoren nil og alt
// oppfører seg som før (fail-open, alltid).
type intentState struct {
	mu     sync.RWMutex
	engine *intent.Engine

	// Avgjørelses-cache: melding → ferdig ruting. Gjør sticky-oppslaget av
	// forrige melding gratis (den ble rutet i forrige tur) og stopper
	// dobbeltarbeid på identiske meldinger. Liten og selvtømmende.
	cacheMu sync.Mutex
	cache   map[string]cachedDecision
}

type cachedDecision struct {
	d  intent.Decision
	at time.Time
}

const (
	decisionCacheTTL = 10 * time.Minute
	decisionCacheMax = 1000
)

// resolveCached ruter via cache: treff gir lagret avgjørelse med null latens.
func (st *intentState) resolveCached(ctx context.Context, eng *intent.Engine, msg string, isAdmin bool) intent.Decision {
	key := msg
	if isAdmin {
		key = "a\x00" + msg
	}
	st.cacheMu.Lock()
	if c, ok := st.cache[key]; ok && time.Since(c.at) < decisionCacheTTL {
		st.cacheMu.Unlock()
		return c.d
	}
	st.cacheMu.Unlock()
	d := eng.Resolve(ctx, msg, isAdmin)
	// Feil-avgjørelser (fail-open none uten kandidater) caches ikke — de skal
	// prøves på nytt neste gang.
	if d.Method == intent.MethodNone && len(d.Candidates) == 0 {
		return d
	}
	st.cacheMu.Lock()
	if st.cache == nil || len(st.cache) > decisionCacheMax {
		st.cache = map[string]cachedDecision{}
	}
	st.cache[key] = cachedDecision{d: d, at: time.Now()}
	st.cacheMu.Unlock()
	return d
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
	countIntent := func(ctx context.Context, model string, in, out int) {
		s.countLLM(ctx, model, "intent", in, out)
	}
	embedder := &intent.ScalewayEmbedder{
		BaseURL: s.cfg.UpstreamBaseURL, APIKey: s.cfg.UpstreamAPIKey,
		Model: "qwen3-embedding-8b", Client: s.client,
		OnUsage: countIntent,
	}
	judge := &intent.ScalewayJudge{
		BaseURL: s.cfg.UpstreamBaseURL, APIKey: s.cfg.UpstreamAPIKey,
		Model: router.MidModel, Client: s.client,
		OnUsage: countIntent,
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

// flowWantsKnowledge leser den rutede flyten fra payloaden og svarer om den
// skal ha intern kunnskap injisert (Flow.Knowledge). Ingen ruting satt
// (fail-open, widget/connector-moduser) = ja — dagens oppførsel.
func flowWantsKnowledge(full map[string]any) bool {
	key, _ := full[flowKeyField].(string)
	if key == "" {
		return true
	}
	f, ok := intent.Flows[key]
	return !ok || f.Knowledge
}

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

// flowModel mapper flyt-radens modellnivå til router-konstantene. "light"
// er bak LIGHT_TIER-bryteren: av (default) ruter de lette flytene til mid
// som før — piloten skal kunne skrus av i prod uten deploy av ny kode.
func (s *Server) flowModel(level string) string {
	switch level {
	case "light":
		if s.cfg.LightTier {
			return router.LightModel
		}
		return router.MidModel
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
// applyIntent ruter meldingen innenfor kallerens tidsbudsjett. Returnerer et
// ferdig blokk-svar for deterministiske flyter, og ellers en apply-funksjon
// som kalleren kjører for å skru flyt-kontrakten på payloaden (mutasjon skjer
// hos kalleren så ruting og kunnskapsoppslag kan kjøre parallelt uten race).
func (s *Server) applyIntent(ctx context.Context, user store.User, full map[string]any) (block string, apply func()) {
	eng := s.intent.get()
	msg := lastUserText(full)
	if msg == "" {
		return "", nil
	}
	// Ren småprat (takk/hilsen/farvel): ferdig svar i kode — momentant, med
	// personlighet, og uten et eneste modell- eller motorkall.
	if canned := cannedSmalltalk(msg); canned != "" {
		return canned, nil
	}
	if eng == nil {
		return "", nil
	}
	d := s.intent.resolveCached(ctx, eng, msg, user.Role == "admin")

	// Sticky v1: en KORT oppfølging uten klar egen intent (ikke direct) arver
	// forrige brukermeldings flyt hvis den er Sticky («sikker?» etter et
	// dataspørsmål skal beholde db-verktøyene). Stateless: forrige melding
	// ligger i payloaden og re-rutes — koster ett ekstra motor-kall kun for
	// korte, usikre oppfølginger.
	// Også et DIREKTE smalltalk/fri chat-treff regnes som svakt for korte
	// oppfølginger: «hva ser du?» ligner smalltalk-eksemplene tekstlig, men i
	// en datadialog er det et dataspørsmål.
	weak := d.Method != intent.MethodDirect || d.Key == "" || d.Key == "smalltalk"
	if weak && len([]rune(strings.TrimSpace(msg))) <= 60 {
		if prev := prevUserText(full); prev != "" {
			pd := s.intent.resolveCached(ctx, eng, prev, user.Role == "admin")
			if pk, pf := intent.FlowFor(pd); pf.Sticky && pd.Key != "" {
				d = intent.Decision{Key: pk, Method: intent.MethodSticky,
					Candidates: d.Candidates, Elapsed: d.Elapsed + pd.Elapsed}
			} else if d.Method == intent.MethodAsk || d.Method == intent.MethodNone ||
				d.Method == intent.MethodMulti || d.Key == "smalltalk" {
				// Midt i en vanlig samtale: en RETNINGSLØS kort oppfølging
				// (ask/none/multi/smalltalk) skal FORTSETTE samtalen (fri
				// chat, alle verktøy) — aldri kapres av panel eller
				// oppklaringsspørsmål («restartet, men fungerer ikke» fikk
				// «vil du sette opp M365 …?»). Et tydelig dommer-valg av
				// konkret flyt beholdes.
				d = intent.Decision{Method: intent.MethodSticky,
					Candidates: d.Candidates, Elapsed: d.Elapsed + pd.Elapsed}
			}
		}
	}
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
	// Dommeren er reelt i tvil: still ett kort oppklaringsspørsmål bygget av
	// toppkandidatenes merkelapper — ren tekst, ingen gjetning.
	if d.Method == intent.MethodAsk && len(d.Candidates) >= 2 {
		a := intent.AskLabels[d.Candidates[0].Key]
		b := intent.AskLabels[d.Candidates[1].Key]
		if a != "" && b != "" && a != b {
			return "Bare så jeg treffer riktig: vil du " + a + ", eller " + b + "?", nil
		}
	}

	key, flow := intent.FlowFor(d)
	s.log.Info("intent", "key", key, "method", d.Method, "ms", d.Elapsed.Milliseconds())

	if flow.Deterministic {
		if panel, ok := deterministicPanels[key]; ok {
			return "```admin\n" + panel + "\n```", nil
		}
		if key == "connect_database" {
			return credentialBlock(msg), nil
		}
		if key == "connect_m365" {
			return s.m365SetupBlock(user), nil
		}
		// Deterministisk uten server-løype: fri chat (fallback).
		return "", nil
	}
	// Flyt-kontrakt: mutasjonen utsettes til kalleren (parallell-trygt).
	return "", func() {
		full[flowKeyField] = key
		if flow.MaxChars > 0 {
			full[flowMaxField] = flow.MaxChars
		}
		full["model"] = s.flowModel(flow.Model)
	}
}

// m365SetupBlock er det deterministiske oppskrift-steget for connect_m365:
// koden velger riktig steg ut fra faktisk tilstand — modellen improviserer
// aldri oppsettet. Panelene (m365auth/m365app) eier alt sensitivt.
func (s *Server) m365SetupBlock(user store.User) string {
	if acc, err := s.store.M365Account(user.ID); err == nil {
		return "Microsoft 365 er allerede koblet til som " + acc.Email + "."
	}
	if _, _, _, ok := s.msAppCreds(user.TenantID); ok {
		return "La oss koble deg til Microsoft 365 — logg inn her:\n```m365auth\n```"
	}
	return "Først må bedriftens Azure-app registreres (engangsjobb, ca. 5 minutter):\n\n" +
		"1. Gå til portal.azure.com, velg App registrations og New registration. Navn: «Nordavind».\n" +
		"2. Under Redirect URI: velg Web og lim inn " + s.msRedirectURL() + "\n" +
		"3. API permissions, Add, Microsoft Graph, Delegated: User.Read, Files.ReadWrite, offline_access.\n" +
		"4. Certificates & secrets, New client secret — kopier verdien med en gang.\n\n" +
		"Legg verdiene inn her, så starter innloggingen automatisk etterpå:\n```m365app\n```"
}

// prevUserText gir nest siste brukermelding i payloaden ("" om ingen).
func prevUserText(full map[string]any) string {
	msgs, _ := full["messages"].([]any)
	seen := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		seen++
		if seen < 2 {
			continue
		}
		if c, ok := m["content"].(string); ok {
			return c
		}
		return ""
	}
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
