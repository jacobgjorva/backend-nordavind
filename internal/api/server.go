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
		client:   &http.Client{Timeout: 0},
		log:      log,
		search:   search.NewClient(),
		store:    st,
		pricing:  newPricing(),
		rates:    &usdNok{},
		credsKey: key,

		planBuilding:   map[string]bool{},
		runActive:      map[string]bool{},
	}
	s.startScheduler(context.Background())
	s.initIntentEngine()
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
	mux.HandleFunc("PATCH /v1/chats/{id}", s.requireAuth(s.handleRenameChat))
	mux.HandleFunc("PUT /v1/chats/{id}/folder", s.requireAuth(s.handleSetChatFolder))
	mux.HandleFunc("POST /v1/export/xlsx", s.requireAuth(s.handleExportXLSX))
	mux.HandleFunc("POST /v1/export/live", s.requireAuth(s.handleCreateLiveExport))
	mux.HandleFunc("POST /v1/export/onedrive", s.requireAuth(s.handleExportOneDrive))
	mux.HandleFunc("GET /v1/m365/status", s.requireAuth(s.handleM365Status))
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
	mux.HandleFunc("GET /v1/admin/users", s.requireAdmin(s.handleAdminListUsers))
	mux.HandleFunc("POST /v1/admin/users", s.requireAdmin(s.handleAdminCreateUser))
	mux.HandleFunc("DELETE /v1/admin/users/{id}", s.requireAdmin(s.handleAdminDeleteUser))
	mux.HandleFunc("POST /v1/chat/completions", s.requireAuth(s.handleChatCompletions))
	mux.HandleFunc("POST /v1/extract", s.requireAuth(s.handleExtract))
	mux.HandleFunc("POST /v1/corrections", s.requireAuth(s.handleLogCorrection))
	mux.HandleFunc("POST /v1/knowledge/extract", s.requireAuth(s.handleExtractKnowledge))
	mux.HandleFunc("POST /v1/documents", s.requireAuth(s.handleCreateDocument))
	mux.HandleFunc("GET /v1/documents", s.requireAuth(s.handleListDocuments))
	mux.HandleFunc("POST /v1/documents/classify", s.requireAuth(s.handleClassifyDocument))
	mux.HandleFunc("DELETE /v1/documents/{id}", s.requireAuth(s.handleDeleteDocument))
	mux.HandleFunc("GET /v1/knowledge/pending", s.requireAdmin(s.handleListPending))
	mux.HandleFunc("GET /v1/knowledge/graph", s.requireAuth(s.handleKnowledgeGraph))
	mux.HandleFunc("POST /v1/knowledge/{id}/accept", s.requireAdmin(s.handleAcceptNode))
	mux.HandleFunc("POST /v1/knowledge/{id}/reject", s.requireAdmin(s.handleRejectNode))
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
	mux.HandleFunc("POST /v1/designs/{slug}/patch", s.requireAuth(s.handleDesignPatch))
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
			full["reasoning"] = map[string]any{"enabled": false}
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
const routingBudget = 900 * time.Millisecond

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
				"content": "I dag er " + time.Now().Format("2006-01-02") + ". " +
					"Svar KORTEST MULIG, men alltid i hele, naturlige setninger — aldri telegramstil eller " +
					"ettordssvar: «Hvor mange cm er det i en meter?» → «En meter er 100 cm.» Ferdig. Ingen innramming, " +
					"kontekst, forbehold eller oppfølgingstilbud med mindre brukeren ber om det. Trengs substans: " +
					"legg det viktigste i FØRSTE setning, si hvert poeng bare ÉN gang, og STOPP straks verdien er " +
					"levert — ikke fyll opp mot noe tak. Null fyll og tomme forbehold. " +
					"Selv når du har mye data (f.eks. etter research): ALDRI en punkt-for-punkt-gjennomgang av flere " +
					"ting — velg det viktigste og gi anbefalingen, ikke en rapport. " +
					"Kun løpende tekst, aldri overskrifter eller lister. UNNTAK: ber brukeren eksplisitt om en " +
					"tabell (eller struktur), svar med en ```table kodeblokk med JSON {\"columns\":[...],\"rows\":[[...]]} " +
					"som inneholder radene fra dataene — maks én kort setning i tillegg. Gi kun svaret: ingen tankerekke, " +
					"innledning eller oppsummering. GJETT ALDRI på fakta. For ENHVER konkret opplysning om " +
					"virkeligheten — navn, tall, datoer, priser, statistikk, hendelser, «hvem/når/hvor mye/" +
					"nyeste/hvor» — skal du anta at du IKKE vet det sikkert og bruke web_search FØR du svarer, " +
					"også når spørsmålet virker trivielt. Kun ren logikk/regning/språk du er helt sikker på kan " +
					"besvares uten søk. Dekker ikke kildene svaret, si det heller enn å gjette. Skriv aldri " +
					"URL-er eller kildehenvisninger fra websøk, de " +
					"vises automatisk. UNNTAK: OneDrive/SharePoint-lenker (url fra m365-verktøyene) SKAL deles " +
					"som klikkbar markdown-lenke når brukeren vil åpne eller ha fila. " +
					"UNNTAK 2: spørsmål om bedriftens egne data (ordre, kunder, salg, tall) besvares ALLTID " +
					"ved å kjøre query_database — aldri web_search, aldri hukommelse, og aldri påstå manglende " +
					"tilgang uten å ha prøvd verktøyet. Ber brukeren om en tabell eller liste over rader: vis " +
					"resultatet med show_table, ikke beskriv radene i prosa. " +
					"HANDLINGSREGEL: har du et verktøy som kan utføre eller sjekke det brukeren spør om, KJØR det " +
					"med en gang — spør ALDRI «vil du at jeg skal …» eller om lov/tillatelse for søk og lesing. " +
					"Brukeren har allerede gitt tillatelsen ved å spørre; handlingen er svaret. " +
					"Får du et bekreftelsesspørsmål («sikker?», «stemmer det?»): bekreft eller korriger med en NY " +
					"formulering og nevn gjerne grunnlaget — ALDRI gjenta forrige svar ordrett. " +
					"Ved råd: land én tydelig anbefaling. Kun hvis forespørselen er for " +
					"vag: still ett oppklarende spørsmål. Tone: avslappet og lun som en trygg kollega, " +
					"uformell men aldri på bekostning av korthet eller presisjon. Bruk kun naturlige " +
					"norske uttrykk, aldri direkte oversatt engelsk slang. Du kan tolke bilder brukeren " +
					"laster opp via bindersen, si aldri at du ikke kan se bilder.",
			}
			// Few-shot: faste eksempel-utvekslinger som VISER svarstilen —
			// langt mer robust enn instrukser alene for akkurat stil.
			// Bildemeldinger er i praksis sin egen flyt: vision-modellen skal
			// BESKRIVE, og korthets-eksemplene gjør den ordknapp til det
			// meningsløse («T.») — de utelates derfor for bilder.
			fewshot := []any{
				map[string]any{"role": "user", "content": "Hvor mange cm er det i en meter?"},
				map[string]any{"role": "assistant", "content": "En meter er 100 cm."},
				map[string]any{"role": "user", "content": "Opprett en ny kobling"},
				map[string]any{"role": "assistant", "content": "Hva skal vi koble til?"},
				map[string]any{"role": "user", "content": "hva er mva-satsen i Norge?"},
				map[string]any{"role": "assistant", "content": "Standardsatsen er 25 %, med 15 % på mat og 12 % på persontransport."},
			}
			if router.LastUserHasImage(payload.Messages) {
				full["messages"] = append([]any{system}, raw...)
			} else {
				full["messages"] = append(append([]any{system}, fewshot...), raw...)
			}
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
		if model == router.TopModel {
			// Tornado (GLM) resonnerer på de tyngste oppgavene.
			full["reasoning"] = map[string]any{"enabled": true}
		}
	}
	// Kun TopModel skal resonnere. Slå reasoning eksplisitt av for alle andre
	// (ikke stol på upstream-default) med mindre klienten selv har satt det.
	if model != router.TopModel {
		if _, set := full["reasoning"]; !set {
			full["reasoning"] = map[string]any{"enabled": false}
		}
	}
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
