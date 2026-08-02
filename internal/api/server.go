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
	"sync"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/search"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Server ruter OpenAI-kompatible forespørsler videre til upstream-endepunktet.
type Server struct {
	cfg      config.Config
	client   *http.Client
	log      *slog.Logger
	search   *search.Client
	store    *store.Store
	pricing  *pricing
	rates    *usdNok
	credsKey []byte // krypteringsnøkkel for kundens databasekredensialer

	// Kontinuerlige oppdrags-løkker som kjører akkurat nå (agent-id → aktiv),
	// så vi ikke starter samme oppdrag to ganger.

	// Spinup-jobber som pågår (agent-id → bygger), så samme plan aldri
	// kompileres to ganger samtidig.
	planMu       sync.Mutex
	planBuilding map[string]bool

	// Rutinekjøringer som pågår akkurat nå (agent-id → kjører) — kun for
	// live-tilstanden i farmen, ingen styring.
	runMu     sync.Mutex
	runActive map[string]bool

	// Intent-motoren (INTENT_ENGINE=shadow) — nil til den er bygget.
	intent intentState

	// Midlertidige e-postvedlegg (Excel fra spørringer) frem til brukeren
	// klikker Send.
	mailAttach attachmentStore
}

func NewServer(cfg config.Config, log *slog.Logger, st *store.Store) *Server {
	key, err := connector.LoadKey(cfg.DBPath)
	if err != nil {
		log.Error("kunne ikke laste krypteringsnøkkel", "err", err)
	}
	s := &Server{
		cfg: cfg,
		// Ingen total timeout: streaming-svar kan stå åpne lenge.
		client:   &http.Client{Timeout: 0, Transport: &retryTransport{base: http.DefaultTransport}},
		log:      log,
		search:   search.NewClient(cfg.SearxURL, cfg.SerperKey),
		store:    st,
		pricing:  newPricing(),
		rates:    &usdNok{},
		credsKey: key,

		planBuilding: map[string]bool{},
		runActive:    map[string]bool{},
	}
	// Blokkeringsovervåking: motorer SearXNG melder som døde logges — dette
	// er metrikken som avgjør når IP-pool/vekting må justeres.
	s.search.Downed = func(engines []string) {
		s.log.Warn("søkemotorer nede i SearXNG", "engines", strings.Join(engines, ","))
	}
	s.startScheduler(context.Background())
	s.initIntentEngine()
	return s
}

// newUpstreamRequest lager en autentisert POST mot Mistral La Plateforme.
//
// ÉN leverandør, ett endepunkt. Girkasser mellom leverandører skjulte hvem
// som faktisk feilet, og kostet oss en hel prod-økt: dommeren i
// intent-motoren sto igjen på feil vert med et felt Mistral ikke godtar,
// feilet på hver eneste tur, og all ruting falt stille til fri chat.
func (s *Server) newUpstreamRequest(ctx context.Context, body io.Reader) (*http.Request, error) {
	return s.newUpstreamPath(ctx, "/chat/completions", body)
}

// newUpstreamPath: autentisert POST mot et vilkårlig Mistral-endepunkt
// (OCR bor på /ocr).
func (s *Server) newUpstreamPath(ctx context.Context, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(s.cfg.UpstreamBaseURL, "/")+path, body)
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
	mux.HandleFunc("PATCH /v1/chats/{id}", s.requireAuth(s.handleRenameChat))
	mux.HandleFunc("PUT /v1/chats/{id}/folder", s.requireAuth(s.handleSetChatFolder))
	mux.HandleFunc("POST /v1/export/xlsx", s.requireAuth(s.handleExportXLSX))
	mux.HandleFunc("POST /v1/export/live", s.requireAuth(s.handleCreateLiveExport))
	mux.HandleFunc("POST /v1/export/onedrive", s.requireAuth(s.handleExportOneDrive))
	mux.HandleFunc("GET /v1/m365/status", s.requireAuth(s.handleM365Status))
	mux.HandleFunc("POST /v1/m365/syncdocs", s.requireAuth(s.handleM365SyncDocs))
	mux.HandleFunc("GET /v1/m365/connect", s.requireAuth(s.handleM365Connect))
	mux.HandleFunc("DELETE /v1/m365", s.requireAuth(s.handleM365Disconnect))
	mux.HandleFunc("POST /v1/m365/app", s.requireAdmin(s.handleSaveM365App))
	// OAuth-callback kommer fra Microsofts redirect — ingen sesjon, state-vernet.
	mux.HandleFunc("GET /v1/m365/callback", s.handleM365Callback)
	mux.HandleFunc("GET /v1/export/links", s.requireAuth(s.handleListExportLinks))
	mux.HandleFunc("DELETE /v1/export/links/{id}", s.requireAuth(s.handleRevokeExportLink))
	// Live-lenkene autentiseres av selve tokenet (Excel har ingen sesjon).
	mux.HandleFunc("GET /v1/live/{token}/data.xlsx", s.handleLiveXLSX)
	mux.HandleFunc("GET /v1/live/{token}/table", s.handleLiveHTML)
	mux.HandleFunc("GET /v1/folders", s.requireAuth(s.handleListFolders))
	mux.HandleFunc("POST /v1/folders", s.requireAuth(s.handleCreateFolder))
	mux.HandleFunc("PATCH /v1/folders/{id}", s.requireAuth(s.handleRenameFolder))
	mux.HandleFunc("DELETE /v1/folders/{id}", s.requireAuth(s.handleDeleteFolder))
	mux.HandleFunc("GET /v1/connections", s.requireAdmin(s.handleListConnections))
	mux.HandleFunc("POST /v1/connections", s.requireAdmin(s.handleCreateConnection))
	mux.HandleFunc("POST /v1/connections/test", s.requireAdmin(s.handleTestConnection))
	mux.HandleFunc("DELETE /v1/connections/{id}", s.requireAdmin(s.handleDeleteConnection))
	mux.HandleFunc("GET /v1/connections/{id}/schema", s.requireAdmin(s.handleConnectionSchema))
	mux.HandleFunc("PUT /v1/connections/{id}/config", s.requireAdmin(s.handleSaveConnectionConfig))
	mux.HandleFunc("PUT /v1/connections/{id}/primary", s.requireAdmin(s.handleSetPrimaryConnection))
	mux.HandleFunc("GET /v1/admin/users", s.requireAdmin(s.handleAdminListUsers))
	mux.HandleFunc("POST /v1/admin/users", s.requireAdmin(s.handleAdminCreateUser))
	mux.HandleFunc("DELETE /v1/admin/users/{id}", s.requireAdmin(s.handleAdminDeleteUser))
	mux.HandleFunc("POST /v1/chat/completions", s.requireAuth(s.handleChatCompletions))
	mux.HandleFunc("POST /v1/extract", s.requireAuth(s.handleExtract))
	mux.HandleFunc("POST /v1/corrections", s.requireAuth(s.handleLogCorrection))
	mux.HandleFunc("POST /v1/knowledge/edges", s.requireAdmin(s.handleCreateEdge))
	mux.HandleFunc("POST /v1/knowledge/edges/delete", s.requireAdmin(s.handleDeleteEdge))
	mux.HandleFunc("POST /v1/knowledge/remember", s.requireAuth(s.handleRememberMessage))
	mux.HandleFunc("POST /v1/knowledge/confirm", s.requireAuth(s.handleConfirmKnowledge))
	mux.HandleFunc("POST /v1/documents", s.requireAuth(s.handleCreateDocument))
	mux.HandleFunc("GET /v1/documents", s.requireAuth(s.handleListDocuments))
	mux.HandleFunc("POST /v1/documents/classify", s.requireAuth(s.handleClassifyDocument))
	mux.HandleFunc("DELETE /v1/documents/{id}", s.requireAuth(s.handleDeleteDocument))
	mux.HandleFunc("GET /v1/knowledge/graph", s.requireAuth(s.handleKnowledgeGraph))
	mux.HandleFunc("GET /v1/entities", s.requireAuth(s.handleEntitySearch))
	mux.HandleFunc("PUT /v1/knowledge/{id}", s.requireAdmin(s.handleUpdateNode))
	mux.HandleFunc("DELETE /v1/knowledge/{id}", s.requireAdmin(s.handleDeleteNode))
	mux.HandleFunc("GET /v1/agents", s.requireAuth(s.handleListAgents))
	mux.HandleFunc("PATCH /v1/agents/{id}/persona", s.requireAuth(s.handleSetAgentPersona))
	mux.HandleFunc("GET /v1/agents/runs", s.requireAuth(s.handleAgentRuns))
	mux.HandleFunc("POST /v1/agents/{id}/seen", s.requireAuth(s.handleMarkAgentSeen))
	mux.HandleFunc("GET /v1/agents/{id}/plan", s.requireAuth(s.handleGetAgentPlan))
	mux.HandleFunc("PUT /v1/agents/{id}/plan", s.requireAuth(s.handleSaveAgentPlan))
	mux.HandleFunc("POST /v1/agents/{id}/plan/rebuild", s.requireAuth(s.handleRebuildAgentPlan))
	mux.HandleFunc("PUT /v1/agents/{id}/schedule", s.requireAuth(s.handleSetAgentSchedule))
	mux.HandleFunc("GET /v1/agent-connections", s.requireAuth(s.handleAgentConnections))
	mux.HandleFunc("GET /v1/mail/account", s.requireAuth(s.handleGetMailAccount))
	mux.HandleFunc("POST /v1/mail/send", s.requireAuth(s.handleMailSend))
	mux.HandleFunc("GET /v1/mail/attachments/{id}", s.requireAuth(s.handleMailAttachment))
	mux.HandleFunc("GET /v1/org/units", s.requireAuth(s.handleListUnits))
	mux.HandleFunc("GET /v1/org/me", s.requireAuth(s.handleOrgMe))
	mux.HandleFunc("GET /v1/org/sharing", s.requireAdmin(s.handleListScopeRequests))
	mux.HandleFunc("POST /v1/org/sharing/{id}", s.requireAdmin(s.handleResolveScopeRequest))
	mux.HandleFunc("POST /v1/org/units", s.requireAdmin(s.handleCreateUnit))
	mux.HandleFunc("PUT /v1/org/units/{id}", s.requireAdmin(s.handleUpdateUnit))
	mux.HandleFunc("DELETE /v1/org/units/{id}", s.requireAdmin(s.handleDeleteUnit))
	mux.HandleFunc("GET /v1/employees", s.requireAuth(s.handleListEmployees))
	mux.HandleFunc("POST /v1/employees", s.requireAdmin(s.handleCreateEmployee))
	mux.HandleFunc("PUT /v1/employees/{id}", s.requireAdmin(s.handleUpdateEmployee))
	mux.HandleFunc("DELETE /v1/employees/{id}", s.requireAdmin(s.handleDeleteEmployee))

	mux.HandleFunc("POST /v1/widgets", s.requireAuth(s.handleCreateWidget))
	mux.HandleFunc("GET /v1/widgets", s.requireAuth(s.handleListWidgets))
	mux.HandleFunc("GET /v1/widgets/{slug}", s.requireAuth(s.handleGetWidget))
	mux.HandleFunc("DELETE /v1/widgets/{slug}", s.requireAuth(s.handleDeleteWidget))
	mux.HandleFunc("POST /v1/widgets/{slug}/save", s.requireAuth(s.handleSaveWidget))
	mux.HandleFunc("POST /v1/widgets/{slug}/share", s.requireAuth(s.handleShareWidget))
	mux.HandleFunc("GET /v1/users", s.requireAuth(s.handleListTenantUsers))
	mux.HandleFunc("GET /v1/widgets/{slug}/query", s.requireAuth(s.handleWidgetQuery))
	mux.HandleFunc("POST /v1/query", s.requireAuth(s.handleAdhocQuery))
	mux.HandleFunc("GET /v1/design/kits", s.requireAuth(s.handleDesignKits))
	mux.HandleFunc("POST /v1/designs", s.requireAuth(s.handleCreateDesign))
	mux.HandleFunc("GET /v1/designs/{chatId}/board", s.requireAuth(s.handleBoard))
	mux.HandleFunc("POST /v1/designs/{chatId}/documents", s.requireAuth(s.handleCreateBoardDocument))
	mux.HandleFunc("POST /v1/designs/{chatId}/documents/{slug}/move", s.requireAuth(s.handleMoveDocument))
	mux.HandleFunc("POST /v1/designs/{slug}/patch", s.requireAuth(s.handleDesignPatch))
	mux.HandleFunc("POST /v1/designs/{slug}/meta", s.requireAuth(s.handleDesignMeta))
	mux.HandleFunc("GET /v1/chats/{chatId}/agent", s.requireAuth(s.handleAgentByChat))
	mux.HandleFunc("PATCH /v1/agents/{id}", s.requireAuth(s.handleSetAgentEnabled))
	mux.HandleFunc("PUT /v1/agents/{id}", s.requireAuth(s.handleUpdateAgent))
	mux.HandleFunc("POST /v1/agents/draft", s.requireAuth(s.handleCreateDraftAgent))
	mux.HandleFunc("POST /v1/agents", s.requireAuth(s.handleCreateAgent))
	mux.HandleFunc("DELETE /v1/agents/{id}", s.requireAuth(s.handleDeleteAgent))
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

	// Intent-motoren: shadow logger bare; on styrer flyten.
	// Et åpent designlerret eier turen selv: instruksen gjelder dokumentet,
	// og skal aldri kapres av ruting (den svarte «skriv /presentasjon» på
	// meldinger som kom fra lerretet).
	_, designMode := full["nordavind_design"]
	if _, widgetMode := full["nordavind_widget"]; !widgetMode && !designMode {
		if _, connMode := full["nordavind_connector"]; !connMode {
			if setup, _ := full["nordavind_agent_setup"].(bool); !setup {
				if user, ok := r.Context().Value(userKey).(store.User); ok {
					switch s.cfg.IntentMode {
					case "shadow":
						s.shadowIntent(user, lastUserText(full))
					case "on":
						// FART: ruting og kunnskapsoppslag kjører PARALLELT med et
						// hardt tidsbudsjett — ingen av dem får noensinne holde
						// modellen tilbake lenger enn budsjettet (fail-open).
						rctx, rcancel := context.WithTimeout(r.Context(), routingBudget)
						type routed struct {
							block string
							apply func()
						}
						rCh := make(chan routed, 1)
						kbCh := make(chan string, 1)
						go func() {
							b, a := s.applyIntent(rctx, user, full)
							rCh <- routed{block: b, apply: a}
						}()
						go func() { kbCh <- s.knowledgeFor(rctx, full) }()
						rd := <-rCh
						kb := <-kbCh
						rcancel()
						if rd.block != "" {
							s.respondSSEBlock(w, rd.block)
							s.log.Info("chat/completions", "mode", "intent-deterministic",
								"dur", time.Since(start).Round(time.Millisecond))
							return
						}
						if rd.apply != nil {
							rd.apply()
						}
						// G3 (docs/KNOWLEDGE.md): kun flyter med Knowledge i
						// flyt-tabellen får intern kunnskap injisert — web_fact
						// og smalltalk skal aldri bære interne lapper.
						if kb != "" && flowWantsKnowledge(full) {
							injectSystem(full, kb)
						}
						if b, err := json.Marshal(full); err == nil {
							patched = b
						}
					}
				}
			}
		}
	}

	// Berik med relevant bransjekunnskap (kun moduser uten parallell-løypa over).
	if s.cfg.IntentMode != "on" {
		if kb := s.knowledgeFor(r.Context(), full); kb != "" {
			injectSystem(full, kb)
			if b, err := json.Marshal(full); err == nil {
				patched = b
			}
		}
	}

	// Streaming-forespørsler kjøres i agent-løkken (web_search som verktøy).
	if wantsStream, _ := full["stream"].(bool); wantsStream {
		// Agent-oppsett startes eksplisitt med /agent (frontend setter flagget).
		// Da kjøres veiviseren på en sterkere modell (Storm), reasoning AV — den
		// skal skrive rent innhold (agent_setup-blokken), ikke resonnere.
		if setup, _ := full["nordavind_agent_setup"].(bool); setup {
			full["model"] = router.HeavyModel
			pickedModel = router.HeavyModel
		}
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

// routingBudget er maksimal tid ruting + kunnskapsoppslag får bruke FORAN
// modellen. Typisk lander begge på 200-500 ms; ved treghet fail-open til
// fri chat i stedet for å forsinke svaret.
// Tidsbudsjettet for ruting OG kunnskapsoppslag. Hevet fra 900 ms fordi
// leverandørens embedding-hale (målt 0,5-21 s fra prod) gjorde at begge falt
// bort på helt vanlige spørsmål. Det hedgede kallet (se embed) gjør at det
// normale tilfellet fortsatt svarer på noen hundre millisekunder — taket
// rammer bare de virkelig trege turene, og fail-open gjelder som før.
const routingBudget = 1800 * time.Millisecond

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
			// Kjernen settes her (tone, korthet, ærlighet). Reglene som
			// avhenger av verktøy legges på i agent-løkka, som er stedet der
			// vi FAKTISK vet hva turen har — den gamle varianten sendte alt
			// på hver melding, 2 667 tegn uansett spørsmål.
			system := map[string]any{
				"role":    "system",
				"content": baseSystem(systemOpts{image: hasImageMessage(full)}),
			}
			// Ingen few-shot-eksempler: de lå som FALSK samtalehistorikk foran
			// hver melding, kostet tokens hver tur, og modellen kunne ikke
			// skille dem fra noe brukeren faktisk hadde sagt — «hva er
			// mva-satsen i Norge?» ble nærmeste nabo til en melding uten
			// referent og smittet svaret. Stilen står i systemteksten.
			full["messages"] = append([]any{system}, raw...)
		}
	}

	model := router.Resolve(payload.Model)
	if model != payload.Model {
		full["model"] = model
	}
	// Et nytt bilde i siste melding krever vision-modellen — overstyrer all
	// annen ruting. Oppfølging uten bilde faller tilbake til Bris/Storm.
	if router.LastUserHasImage(payload.Messages) {
		model = router.VisionModel
		full["model"] = model
	} else if model == "auto" {
		model = router.Pick(payload.Messages)
		full["model"] = model
	}
	// «reasoning» er et Scaleway-felt. Mistral svarer 422 på det, og da
	// feiler HELE turen — brukeren fikk «jeg fikk ikke satt sammen et svar»
	// (målt i prod 2026-07-30). Feltet fjernes her, ved inngangen, så ingen
	// klient eller kodesti kan smugle det videre.
	delete(full, "reasoning")
	// Bris/Storm er ikke multimodale — bytt ut gamle bilde-deler med en
	// tekstplassholder så de kan svare på oppfølging uten å feile på bildedata.
	if model != router.VisionModel {
		stripImageParts(full)
	}
	patched, err := json.Marshal(full)
	if err != nil {
		return body, full, model
	}
	return patched, full, model
}

// stripImageParts erstatter bilde-deler i meldingene med en tekstplassholder,
// slik at ikke-multimodale modeller (Bris/Storm) ikke feiler på bildedata.
// Vision-modellens beskrivelse ligger allerede i samtalen som tekst.
func stripImageParts(full map[string]any) {
	msgs, ok := full["messages"].([]any)
	if !ok {
		return
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		var b strings.Builder
		for _, p := range parts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text":
				if t, ok := pm["text"].(string); ok {
					b.WriteString(t)
				}
			case "image_url":
				b.WriteString(" [bilde sendt tidligere] ")
			}
		}
		mm["content"] = strings.TrimSpace(b.String())
	}
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// retryTransport gjentar 429 og 5xx med eksponentiell backoff.
//
// MÅLT (2026-07-30): Mistral La Plateforme rate-limiter hardt — 93 av 147
// kall i intent-evalen fikk 429. Rutingen falt da tilbake på
// embedding-scoren alene, stille, uten at noe krasjet. Et tapt kall koster
// hele turens metodevalg, så det er verdt tre forsøk.
type retryTransport struct{ base http.RoundTripper }

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
		body = b
	}
	delays := []time.Duration{400 * time.Millisecond, 1200 * time.Millisecond, 3 * time.Second}
	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		r := req.Clone(req.Context())
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		resp, err = t.base.RoundTrip(r)
		retryable := err != nil || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if !retryable || attempt >= len(delays) {
			return resp, err
		}
		if resp != nil {
			resp.Body.Close()
		}
		select {
		case <-time.After(delays[attempt]):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
}
