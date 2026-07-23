package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Modellen bestemmer selv når den trenger nettet — ingen forhåndsdommer.
var webSearchTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "web_search",
		"description": "Søk på nettet. Bruk når svaret krever fersk informasjon eller fakta om " +
			"spesifikke entiteter (personer, selskaper, produkter, steder, hendelser) du ikke er " +
			"sikker på. Gjør ett kall per entitet/tema. Ga ikke resultatene svaret: søk igjen " +
			"med en annen formulering før du gir opp.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Selvstendig søkestreng"},
			},
			"required": []string{"query"},
		},
	},
}

// fetchURLTool lar agenten lese hele innholdet på én konkret side (HTML/PDF),
// ikke bare søketreff-snuttene. Uunnværlig for å hente eksakte tall og fakta.
var fetchURLTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "fetch_url",
		"description": "Hent og les hele innholdet fra ÉN konkret URL (nettside eller PDF). Bruk etter et " +
			"web_search når du trenger de faktiske tallene/detaljene fra en kilde, ikke bare snutten. " +
			"Oppgi en fullstendig url (inkl. https://).",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "Fullstendig URL, f.eks. https://..."},
			},
			"required": []string{"url"},
		},
	},
}

const maxToolRounds = 3

// deepRounds er runde-taket i grundig modus: agenten får grave mye lenger enn
// et vanlig svar (3 runder) før den konkluderer.
const deepRounds = 20

// deepMarkers er signaler på at brukeren vil ha grundig, uforstyrret dybdearbeid
// — da slår vi på grundig modus (mange runder, primærkilder, kryssjekk).
var deepMarkers = []string{
	"grundig", "nøye", "grundig research", "grundig arbeid", "dyp research",
	"dypdykk", "gå i dybden", "i dybden", "ta deg tid", "ta deg god tid",
	"veldig viktig", "svært viktig", "ekstremt viktig", "kjempeviktig",
	"ikke forstyrr", "bare få det gjort", "bare fiks det", "gjør det ordentlig",
	"gjør en skikkelig", "skikkelig jobb", "grav", "kartlegg grundig",
}

// deepIntent er sann når siste brukermelding ber om grundig/uforstyrret arbeid.
func deepIntent(full map[string]any) bool {
	lower := strings.ToLower(lastUserText(full))
	for _, m := range deepMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// deepResearchSystem gjør et vanlig chat-svar til grundig ekspertarbeid: samme
// disiplin som oppdrags-agenten, men konkluderer i chatten når jobben er gjort.
const deepResearchSystem = "GRUNDIG MODUS er på. Grundigheten ligger i ARBEIDET, ikke i lengden på svaret. Du " +
	"er en sulten, skeptisk analytiker som graver dypt, men leverer tett.\n" +
	"ARBEIDET (usynlig for bruker): hver påstand SKAL ha et konkret tall/faktum fra kilde — ingen gjetting. " +
	"web_search finner kilder; gå så til PRIMÆRKILDEN med fetch_url (årsrapport, børsmelding, register, egne " +
	"sider) og les de faktiske tallene, ikke bare snutter. Kryssjekk viktige tall mot minst to uavhengige " +
	"kilder; spriker de, grav videre. Bruk query_database for interne data. Jobb i mange runder, ikke " +
	"konkluder for tidlig, og angrip eget arbeid før du svarer — lukk hullene.\n" +
	"SVARET (det brukeren ser): EKSTREMT tett, som en toppanalytikers brief. Konklusjon/anbefaling FØRST i " +
	"én setning, så bare de FÅ avgjørende tallene og den ene drivende innsikten. IKKE vis regnestykket steg " +
	"for steg, ikke tenk høyt («vent, la oss justere»), ikke gjenta, ingen overskrifter/lister/mellomtitler " +
	"med mindre brukeren ba om det, ingen «her er den detaljerte gjennomgangen». Typisk 2-5 setninger — bare " +
	"det som endrer beslutningen. All research-jobben er gjort for å gjøre disse få setningene RIKTIGE og " +
	"skarpe, ikke for å fylle svaret."

// injectSystem legger en instruks til samtalens system-melding — føyer til
// den første system-meldingen hvis den finnes, ellers settes en ny fremst.
func injectSystem(full map[string]any, text string) {
	msgs, _ := full["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok || mm["role"] != "system" {
			continue
		}
		if c, ok := mm["content"].(string); ok {
			mm["content"] = c + "\n\n" + text
			return
		}
	}
	sys := map[string]any{"role": "system", "content": text}
	full["messages"] = append([]any{sys}, msgs...)
}

// hasImageMessage sjekker om samtalen inneholder en bilde-del.
func hasImageMessage(full map[string]any) bool {
	msgs, _ := full["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "image_url" {
				return true
			}
		}
	}
	return false
}

// runAgentLoop kjører chat-forespørselen med verktøy (web_search, database
// m.fl.). Reasoning og modell-chunks streames til klienten underveis via
// relayRound; når modellen begynner på selve svaret, pipes resten rått gjennom.
func (s *Server) runAgentLoop(ctx context.Context, w http.ResponseWriter, full map[string]any) {
	dbCtx := s.dbToolContext(ctx)

	// Agent-oppsett: /agent-flyten settes av frontend. Vi legger inn en
	// setup-instruks og administrasjonsverktøyene. Flagget må fjernes før
	// forespørselen sendes videre til upstream.
	setup, _ := full["nordavind_agent_setup"].(bool)
	delete(full, "nordavind_agent_setup")
	if setup {
		injectSystem(full, agentSetupSystem)
	}

	// Widget-editor: modellen er ren utfører som bygger én widget via verktøy.
	widgetSlug, _ := full["nordavind_widget"].(string)
	delete(full, "nordavind_widget")
	if widgetSlug != "" {
		injectSystem(full, s.widgetSystem(ctx))
	}

	// Agent-chat: enten oppdrags-planlegging (før kriteriene er godkjent) eller
	// vanlig redigering av en ferdig konfigurert agent.
	editID, _ := full["nordavind_agent_edit"].(string)
	delete(full, "nordavind_agent_edit")
	editable := false
	planning := false
	if editID != "" {
		if user, ok := ctx.Value(userKey).(store.User); ok {
			if a, err := s.store.GetAgent(editID, user.ID); err == nil {
				if !a.CriteriaApproved {
					// Draft: hjelp brukeren definere mål + fullført-kriterier.
					injectSystem(full, missionPlanSystem)
					planning = true
				} else {
					injectSystem(full, agentEditSystem(a))
					editable = true
				}
			}
		}
	}

	// Bilder tolkes av vision-modellen uten verktøy — web_search/db gir
	// tomme svar sammen med bilde-input.
	if !hasImageMessage(full) {
		if widgetSlug != "" {
			// Widget-editor: KUN set_widget. Uten query_database/web_search kan
			// ikke modellen svare på dataspørsmål — den må skrive SQL-en inn i
			// widgeten. Skjemaet ligger allerede i system-prompten.
			full["tools"] = widgetTools()
		} else if setup {
			// Agent-oppsett: KUN agent-verktøy. Ingen web_search/query_database/
			// contact_person — veiviseren skal samle inn oppsettet og la
			// scheduleren utføre jobben etterpå, aldri gjøre oppgaven eller
			// eskalere nå. Opprettelsen skjer via agent_setup-blokken.
			full["tools"] = buildAgentSetupTools()
		} else if planning {
			// Oppdrags-planlegging: KUN start_mission. Modellen forstår oppgaven,
			// utleder selv fullført-kriterier, og starter løkka med det samme.
			full["tools"] = missionStartTools()
		} else {
			tools := []any{webSearchTool, fetchURLTool}
			if dbCtx != nil {
				tools = append(tools, dbCtx.tool)
			}
			// Eskalerings-verktøy (registeret ligger i verktøy-beskrivelsen).
			if user, ok := ctx.Value(userKey).(store.User); ok {
				if ct := s.contactTool(user.TenantID); ct != nil {
					tools = append(tools, ct)
				}
			}
			if editable {
				tools = append(tools, buildAgentEditTools()...)
			}
			full["tools"] = tools
		}
	}
	// Grundig modus: vanlig chat (ikke oppsett/widget/agent-redigering) der
	// brukeren ber om grundig/uforstyrret dybdearbeid → ekspert-prompt og mange
	// runder før den konkluderer i chatten.
	roundCap := maxToolRounds
	if !setup && widgetSlug == "" && editID == "" && !hasImageMessage(full) && deepIntent(full) {
		injectSystem(full, deepResearchSystem)
		roundCap = deepRounds
	}

	full["stream"] = true
	// Be upstream om tokenforbruk i streamen.
	full["stream_options"] = map[string]any{"include_usage": true}

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	emit := func(line string) {
		w.Write([]byte(line + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}

	start := time.Now()
	var promptTokens, completionTokens, searches int
	usedTool := false // om noen verktøy (søk/lese/db) er kjørt — styrer tom-svar-vernet
	defer func() {
		s.recordUsage(ctx, full, promptTokens, completionTokens, searches, time.Since(start))
	}()

	for round := 0; round <= roundCap; round++ {
		body, err := json.Marshal(full)
		if err != nil {
			return
		}
		req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
		if err != nil {
			return
		}
		resp, err := s.client.Do(req)
		if err != nil {
			s.log.Error("upstream-kall feilet", "err", err)
			emit(`data: {"error":"upstream utilgjengelig"}`)
			emit("data: [DONE]")
			return
		}

		// Svaret bufres (streamContent=false): vi måler det FØR vi viser noe, så
		// vi kan fange både tomt svar (backstop) og for lange svar (komprimering)
		// uten å kappe midt i en setning. Arbeids-indikatoren dekker ventetiden.
		calls, usage, content := s.relayRound(resp, emit, false)
		resp.Body.Close()
		promptTokens += usage.PromptTokens
		completionTokens += usage.CompletionTokens
		if len(calls) == 0 {
			trimmed := strings.TrimSpace(content)
			switch {
			case len(trimmed) < 40 && usedTool:
				// (Nesten) tomt etter verktøybruk → synteser fra det den fant.
				step, _ := json.Marshal(map[string]any{"nordavind_step": "Oppsummerer funnene"})
				emit("data: " + string(step))
				s.streamBackstop(ctx, full, emit, &promptTokens, &completionTokens)
			case trimmed == "":
				emit(contentSSE(backstopGraceful))
			case len([]rune(trimmed)) > wallCharLimit:
				// Nødbrems: modellen ignorerte korthet og skrev en vegg →
				// komprimer til noen få HELE setninger (sjelden, minimal sløsing).
				step, _ := json.Marshal(map[string]any{"nordavind_step": "Strammer svaret"})
				emit("data: " + string(step))
				emit(contentSSE(s.compressAnswer(ctx, trimmed)))
			default:
				emit(contentSSE(content))
			}
			emit("data: [DONE]")
			return
		}

		// Eskalering: modellen ba om å kontakte en person. Rendrer compose-kortet
		// deterministisk og avslutter — ingen fri-tekst-blokk å bomme på.
		for _, c := range calls {
			if c.Name != "contact_person" {
				continue
			}
			var ca struct {
				Name    string `json:"name"`
				Email   string `json:"email"`
				Subject string `json:"subject"`
				Body    string `json:"body"`
			}
			_ = json.Unmarshal([]byte(c.Args.String()), &ca)
			if strings.TrimSpace(ca.Email) == "" {
				continue
			}
			emit(contentSSE(composeBlock(ca.Name, ca.Email, ca.Subject, ca.Body)))
			emit("data: [DONE]")
			return
		}

		// Utfør verktøykallene og legg resultatene inn i samtalen.
		usedTool = true
		assistantCalls := make([]any, 0, len(calls))
		toolMsgs := make([]any, 0, len(calls))
		for _, c := range calls {
			var args struct {
				Query        string `json:"query"`
				URL          string `json:"url"`
				ConnectionID string `json:"connection_id"`
				SQL          string `json:"sql"`
			}
			_ = json.Unmarshal([]byte(c.Args.String()), &args)

			var result string
			switch c.Name {
			case "query_database":
				meta, _ := json.Marshal(map[string]any{"nordavind_step": "Spør databasen"})
				emit("data: " + string(meta))
				result = s.runDBQuery(ctx, dbCtx, args.ConnectionID, args.SQL)
			case "fetch_url":
				step := "Leser en side"
				if u := strings.TrimSpace(args.URL); u != "" {
					step = "Leser: " + u
				}
				meta, _ := json.Marshal(map[string]any{"nordavind_step": step})
				emit("data: " + string(meta))
				result = s.runFetchURL(ctx, args.URL)
			case "create_agent", "update_agent", "delete_agent", "list_agents":
				var aa agentToolArgs
				_ = json.Unmarshal([]byte(c.Args.String()), &aa)
				switch c.Name {
				case "create_agent":
					meta, _ := json.Marshal(map[string]any{"nordavind_step": "Oppretter agent"})
					emit("data: " + string(meta))
					result = s.runCreateAgent(ctx, aa)
				case "update_agent":
					meta, _ := json.Marshal(map[string]any{"nordavind_step": "Oppdaterer agent"})
					emit("data: " + string(meta))
					result = s.runUpdateAgent(ctx, aa)
				case "delete_agent":
					result = s.runDeleteAgent(ctx, aa)
				default:
					result = s.runListAgents(ctx)
				}
			case "start_mission":
				var m struct {
					Goal     string `json:"goal"`
					Criteria string `json:"criteria"`
				}
				_ = json.Unmarshal([]byte(c.Args.String()), &m)
				result = s.runStartMission(ctx, editID, m.Goal, m.Criteria)
			case "set_widget":
				meta, _ := json.Marshal(map[string]any{"nordavind_step": "Bygger widget"})
				emit("data: " + string(meta))
				result = s.runWidgetOp(ctx, widgetSlug, c.Args.String())
				emit(`data: {"nordavind_widget_updated":true}`)
			default:
				if q := strings.TrimSpace(args.Query); q != "" {
					// Fremdriftssteg til tidslinjen i frontend.
					meta, _ := json.Marshal(map[string]any{"nordavind_step": "Søker: " + q})
					emit("data: " + string(meta))
				}
				searches++
				var sources []sourceRef
				result, sources = s.runWebSearch(ctx, args.Query)
				if len(sources) > 0 {
					// Kildene sendes som metadata til frontend — de skal ikke stå i svaret.
					meta, _ := json.Marshal(map[string]any{"nordavind_sources": sources})
					emit("data: " + string(meta))
				}
			}
			assistantCalls = append(assistantCalls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": c.Args.String(),
				},
			})
			toolMsgs = append(toolMsgs, map[string]any{
				"role":         "tool",
				"tool_call_id": c.ID,
				"content":      result,
			})
		}
		msgs, _ := full["messages"].([]any)
		msgs = append(msgs, map[string]any{
			"role":       "assistant",
			"content":    "",
			"tool_calls": assistantCalls,
		})
		msgs = append(msgs, toolMsgs...)
		full["messages"] = msgs

		if round == roundCap-1 {
			// Siste runde: tving frem et svar uten flere verktøykall, og be
			// modellen tydelig om å svare NÅ fra det den har funnet — ikke tomt.
			full["tool_choice"] = "none"
			injectSystem(full, backstopNudge)
		}
	}
	// Løkka gikk tom for runder uten et rent svar (modellen ville søke videre).
	// Synteser fra det den alt har hentet, så bruker aldri sitter igjen med blankt.
	step, _ := json.Marshal(map[string]any{"nordavind_step": "Oppsummerer funnene"})
	emit("data: " + string(step))
	s.streamBackstop(ctx, full, emit, &promptTokens, &completionTokens)
	emit("data: [DONE]")
}

// recordUsage lagrer forbruket for forespørselen. Uinnlogget (dev) logges
// uten tenant/bruker.
func (s *Server) recordUsage(ctx context.Context, full map[string]any, promptTokens, completionTokens, searches int, dur time.Duration) {
	if promptTokens == 0 && completionTokens == 0 {
		return
	}
	model, _ := full["model"].(string)
	event := store.UsageEvent{
		Model:            model,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostUSD:          s.pricing.cost(model, promptTokens, completionTokens),
		Searches:         searches,
		DurationMS:       dur.Milliseconds(),
	}
	if user, ok := ctx.Value(userKey).(store.User); ok {
		event.TenantID = user.TenantID
		event.UserID = user.ID
	}
	if err := s.store.InsertUsage(event); err != nil {
		s.log.Warn("kunne ikke lagre usage", "err", err)
	}
}

// sourceRef er kildemetadata som streames til frontend.
type sourceRef struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// wallCharLimit: over dette regnes svaret som en «vegg» og komprimeres (nødbrems).
const wallCharLimit = 600

const compressSystem = "Komprimer teksten under til MAKS 3 korte, HELE setninger på norsk. Anbefaling/" +
	"konklusjon først, deretter bare det aller viktigste. Behold nøkkeltall og navn. ALDRI liste, " +
	"overskrifter eller punkt-for-punkt. Behold samme mening, bare tettere. Svar kun med den komprimerte teksten."

// compressAnswer strammer et for langt svar til noen få hele setninger. Feiler
// kallet, returneres originalen (heller en vegg enn blankt — men svært sjelden).
func (s *Server) compressAnswer(ctx context.Context, text string) string {
	out, err := s.llmComplete(ctx, compressSystem, text, 260)
	if err != nil || strings.TrimSpace(out) == "" {
		return text
	}
	return strings.TrimSpace(out)
}

// backstopNudge ber modellen konkludere fra det den alt har hentet, aldri tomt.
const backstopNudge = "Verktøyene er ikke lenger tilgjengelige. Svar NÅ i MAKS 2-3 korte setninger med KUN det " +
	"viktigste fra det du har funnet — anbefaling/konklusjon først. ALDRI en liste eller punkt-for-punkt-" +
	"gjennomgang av flere ting, ALDRI en lang oppsummering. Velg det ene som betyr mest. Svar aldri tomt."

// backstopGraceful er den siste, varme utveien hvis alt annet gir tomt.
const backstopGraceful = "Jeg fant en del, men rakk ikke å lande det rent nå. Vil du at jeg går grundig " +
	"til verks på dette?"

// streamBackstop kjøres når et svar kom tomt: ett synteser-kall (uten verktøy) på
// samtalen slik den står — all verktøy-data er alt i konteksten. Gir det fortsatt
// tomt, sendes en kort, vennlig linje. Bruker ser aldri en blank boble.
func (s *Server) streamBackstop(ctx context.Context, full map[string]any, emit func(string), promptTokens, completionTokens *int) {
	injectSystem(full, backstopNudge)
	full["tool_choice"] = "none"
	delete(full, "tools")
	body, err := json.Marshal(full)
	if err != nil {
		emit(contentSSE(backstopGraceful))
		return
	}
	req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
	if err != nil {
		emit(contentSSE(backstopGraceful))
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		emit(contentSSE(backstopGraceful))
		return
	}
	_, usage, content := s.relayRound(resp, emit, true)
	resp.Body.Close()
	*promptTokens += usage.PromptTokens
	*completionTokens += usage.CompletionTokens
	if len(strings.TrimSpace(content)) < 40 {
		emit(contentSSE(backstopGraceful))
	}
}

// runFetchURL henter hele det lesbare innholdet fra én side (dypere enn søk).
func (s *Server) runFetchURL(ctx context.Context, url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return "Mangler url."
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	text := s.search.FetchURL(ctx, url, 6000)
	if strings.TrimSpace(text) == "" {
		return "Fant ikke lesbart innhold på siden."
	}
	return text
}

// runWebSearch utfører søk + sidehenting og formaterer kildekontekst.
func (s *Server) runWebSearch(ctx context.Context, query string) (string, []sourceRef) {
	if strings.TrimSpace(query) == "" {
		return "Tomt søk.", nil
	}
	results, err := s.search.Search(ctx, query)
	if err != nil || len(results) == 0 {
		s.log.Warn("websøk feilet", "query", query, "err", err)
		return "Søket ga ingen resultater.", nil
	}
	if len(results) > 3 {
		results = results[:3]
	}
	pages := s.search.FetchPages(ctx, results, 2800)
	s.log.Info("websøk", "query", query, "treff", len(results))

	refs := make([]sourceRef, 0, len(results))
	for _, r := range results {
		refs = append(refs, sourceRef{Title: r.Title, URL: r.URL})
	}
	return search.FormatContext(query, results, pages), refs
}
