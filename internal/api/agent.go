package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/search"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Modellen bestemmer selv når den trenger nettet — ingen forhåndsdommer.
// widgetSlugRe plukker slugen ut av runWidgetOp-kvitteringen.
var widgetSlugRe = regexp.MustCompile(`slug=([a-z0-9-]+)`)

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

// showTableTool viser siste databasesvar som en rendret tabell for brukeren.
// Modellen sender IKKE dataene selv — backend rendrer dem deterministisk fra
// spørringen, så tabellen kan aldri bli feilskrevet eller utelatt.
var showTableTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "show_table",
		"description": "Vis resultatet av siste query_database-kall som en ferdig tabell for brukeren. " +
			"KALL ALLTID denne når brukeren ber om en tabell, liste eller oversikt over data. Dataene " +
			"hentes automatisk fra spørringen — ikke gjengi radene i tekst.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

// tableBlock bygger ```table-blokken frontend rendrer som tabell (med Excel-knapp).
func tableBlock(cols []string, rows [][]string) string {
	spec, _ := json.Marshal(map[string]any{"columns": cols, "rows": rows})
	return "```table\n" + string(spec) + "\n```\n\n"
}

// tableIntent er sann når brukeren eksplisitt ber om en tabell/oversikt i rader.
func tableIntent(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range []string{"tabell", "table", "liste over", "oversikt over"} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
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
	// Flyt-kontrakten (intent-modus): leses ut her, skal aldri videre upstream.
	flowKey, _ := full[flowKeyField].(string)
	answerLimit := answerCharLimit(full)
	delete(full, flowKeyField)
	delete(full, flowMaxField)
	if setup {
		injectSystem(full, agentSetupSystem)
	}

	// Widget-editor: modellen er ren utfører som bygger én widget via verktøy.
	widgetSlug, _ := full["nordavind_widget"].(string)
	delete(full, "nordavind_widget")
	if widgetSlug != "" {
		injectSystem(full, s.widgetSystem(ctx))
	}
	// Widget-flytene fra intent-ruting trenger samme feltskjema-instruks som
	// widget-editoren — uten den famler modellen blindt med set_widget.
	if widgetSlug == "" && (flowKey == "create_widget" || flowKey == "edit_widget" || flowKey == "create_presentation") {
		injectSystem(full, s.widgetSystem(ctx))
	}

	// Connector-agent: hjelper brukeren koble til eksterne kilder via verktøy.
	connectorMode, _ := full["nordavind_connector"].(bool)
	delete(full, "nordavind_connector")
	if connectorMode {
		injectSystem(full, s.connectorAgentSystem())
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
				if !a.CriteriaApproved && !a.Enabled {
					// Ferskt utkast: avgjør rutine vs engangsoppdrag og start.
					// (En aktivert rutine er Enabled uten godkjente kriterier —
					// den skal til vanlig redigering, ikke ny planlegging.)
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
		} else if connectorMode {
			// Connector-agent: KUN tilkoblings-verktøyene.
			full["tools"] = connectorAgentTools()
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
		} else if flowKey != "" && flowKey != intent.FreeChatKey {
			// Intent-modus: flyt-tabellen bestemmer verktøysettet (flowtools.go).
			// Kan flyten ikke innfris (mangler db/M365): fritt sett, fail-open.
			if tools, ok := s.flowTools(ctx, flowKey, dbCtx); ok {
				if len(tools) > 0 {
					full["tools"] = tools
				} else {
					delete(full, "tools")
				}
			} else if dbCtx == nil && flowNeedsDB(flowKey) {
				// Dataflyt uten datatilgang: svar ærlig i kode — modellen skal
				// aldri dikte «databasen tillater ikke lesing».
				emit0 := func(line string) {
					w.Write([]byte(line + "\n"))
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit0(contentSSE("Du har ikke fått tilgang til bedriftsdataene ennå — be administratoren dele de aktuelle tabellene med deg, så svarer jeg på dette med en gang etterpå."))
				emit0("data: [DONE]")
				return
			} else {
				tools := []any{webSearchTool, fetchURLTool}
				if dbCtx != nil {
					tools = append(tools, dbCtx.tool, showTableTool)
				}
				if _, ok := s.m365Connected(ctx); ok {
					tools = append(tools, m365SearchTool, m365ReadTool)
				}
				full["tools"] = tools
			}
		} else {
			tools := []any{webSearchTool, fetchURLTool}
			if dbCtx != nil {
				tools = append(tools, dbCtx.tool, showTableTool)
			}
			// Admin kan opprette tilkoblinger rett fra vanlig chat — ingen egen
			// connector-chat. Verktøybeskrivelsene bærer veiledningen.
			if user, ok := ctx.Value(userKey).(store.User); ok && user.Role == "admin" {
				tools = append(tools, connectorAgentTools()...)
			}
			// M365-filverktøy kun når brukeren har koblet Microsoft 365.
			if _, ok := s.m365Connected(ctx); ok {
				tools = append(tools, m365SearchTool, m365ReadTool)
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
	// Tilstand slår historikk: har brukeren datatilgang NÅ (uansett flyt),
	// skal gamle avslag i samtalen aldri gjentas — tilgang deles gjerne midt
	// i samtalen («kan du sjekke nå?»).
	if !setup && widgetSlug == "" && !connectorMode && !planning && dbCtx != nil {
		injectSystem(full, "VIKTIG: Brukeren HAR datatilgang akkurat nå (query_database er tilgjengelig "+
			"med skjemaet i verktøyet). Eventuelle påstander tidligere i samtalen om manglende tilgang "+
			"eller databasefeil er UTDATERTE — ignorer dem, kjør spørringen og svar med ferske tall.")
		scrubStaleRefusals(full)
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
	// Slug for widget opprettet i denne turen (create_widget-flyten) — blokka
	// appendes i KODE hvis modellen glemmer den; widgeten skal alltid vises.
	createdWidget := ""
	// Datavern mot fabrikkerte tall: feiler ALLE databasespørringene i turen,
	// overstyres svaret i kode — modellen diktet en «nyligste salgsrad» etter
	// tre timeouts.
	dbAttempted, dbSucceeded := false, false
	// Handlings-verktøy (endrer tilstand: rutine, widget, agent, m365 …) gir
	// korte kvitteringer med vilje — de skal ALDRI utløse backstop-syntesen.
	actionTool := false
	readTools := map[string]bool{
		"web_search": true, "fetch_url": true, "query_database": true,
		"m365_search": true, "m365_read": true, "list_agents": true, "show_table": true,
	}
	// Tabell-garanti: show_table-verktøyet rendrer siste databasesvar
	// deterministisk, og ba brukeren om tabell rendrer vi uansett fra første
	// svar med rader — prompt alene er ikke til å stole på her.
	wantsTable := tableIntent(lastUserText(full))
	tableShown := false
	var lastCols []string
	var lastRows [][]string
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

		// FART: svaret STRØMMES live (hybrid) — brukeren ser hvert ord i det
		// det skrives. Kun spesialtilfeller bufres: widget-flyter (blokk-
		// garantien) og turer der databasen har feilet (datavernet må kunne
		// overstyre HELE svaret).
		streamThis := !(dbAttempted && !dbSucceeded) &&
			flowKey != "create_widget" && flowKey != "edit_widget" &&
			flowKey != "create_presentation" && widgetSlug == ""
		calls, usage, content := s.relayRound(resp, emit, streamThis)
		resp.Body.Close()
		promptTokens += usage.PromptTokens
		completionTokens += usage.CompletionTokens
		if len(calls) == 0 {
			trimmed := strings.TrimSpace(content)
			// Datavern: databasen ble forsøkt men ga aldri ett eneste svar —
			// da finnes det ingen tall å gjengi. Ærlig feilmelding i kode i
			// stedet for modellens tekst (som kan være fabrikkert).
			if dbAttempted && !dbSucceeded {
				emit(contentSSE("Databasen svarte ikke i tide på dette, så jeg har ingen tall å gi deg akkurat nå — prøv igjen om et lite øyeblikk."))
				emit("data: [DONE]")
				return
			}
			if streamThis {
				// Alt synlig er allerede sendt live. Kun tomhets-backstop og
				// grense-LOGG igjen (aldri omskriving av noe brukeren har sett).
				switch {
				case len([]rune(trimmed)) < 3 && usedTool && !actionTool:
					step, _ := json.Marshal(map[string]any{"nordavind_step": "Oppsummerer funnene"})
					emit("data: " + string(step))
					s.streamBackstop(ctx, full, emit, &promptTokens, &completionTokens)
				case trimmed == "" && !actionTool:
					emit(contentSSE(backstopGraceful))
				case len([]rune(trimmed)) > answerLimit:
					s.log.Info("svar over myk grense (streamet)", "tegn", len([]rune(trimmed)), "grense", answerLimit)
				}
				emit("data: [DONE]")
				return
			}
			switch {
			case len([]rune(trimmed)) < 3 && usedTool && !actionTool:
				// Reelt tomt (eller bare tegnsetting) etter verktøybruk —
				// korte, gyldige svar («426 ordre.») skal IKKE hit.
				// (Nesten) tomt etter verktøybruk → synteser fra det den fant.
				step, _ := json.Marshal(map[string]any{"nordavind_step": "Oppsummerer funnene"})
				emit("data: " + string(step))
				s.streamBackstop(ctx, full, emit, &promptTokens, &completionTokens)
			case trimmed == "" && !actionTool:
				emit(contentSSE(backstopGraceful))
			case len([]rune(trimmed)) > answerLimit:
				// Over flytens (eller standard) grense: aldri kutt — skriv om
				// til hele setninger. Statusen er bevisst selvironisk.
				step, _ := json.Marshal(map[string]any{"nordavind_step": "Sorry, dette blir for mye greier, la meg omformulere meg"})
				emit("data: " + string(step))
				emit(contentSSE(s.compressAnswer(ctx, trimmed)))
			default:
				emit(contentSSE(dedupeStutter(content)))
			}
			// Bulletproof: ny widget skal ALLTID rendres, selv om modellen
			// glemmer blokka i svaret sitt.
			if createdWidget != "" && !strings.Contains(content, "```widget") {
				emit(contentSSE("\n\n```widget\n" + createdWidget + "\n```"))
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
		for _, c := range calls {
			if !readTools[c.Name] {
				actionTool = true
			}
		}
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
				dbAttempted = true
				if strings.HasPrefix(strings.TrimSpace(result), "{") {
					dbSucceeded = true
				}
				// Send spørringen som metadata så tabellen i svaret kan tilby
				// live Excel-kobling (frontend fester den til meldingen).
				if strings.TrimSpace(args.SQL) != "" {
					qm, _ := json.Marshal(map[string]any{"nordavind_query": map[string]string{
						"connection_id": args.ConnectionID,
						"sql":           args.SQL,
					}})
					emit("data: " + string(qm))
				}
				// Husk siste resultat (til show_table) og rendr tabellen
				// deterministisk med en gang når brukeren ba om tabell.
				var qr struct {
					Columns []string   `json:"columns"`
					Rows    [][]string `json:"rows"`
				}
				if json.Unmarshal([]byte(result), &qr) == nil && len(qr.Columns) > 0 {
					lastCols, lastRows = qr.Columns, qr.Rows
					if wantsTable && !tableShown && len(qr.Rows) > 0 {
						emit(contentSSE(tableBlock(qr.Columns, qr.Rows)))
						tableShown = true
						result += "\n\n(Tabellen er allerede vist til brukeren — IKKE gjengi radene i tekst, legg til maks én kort setning.)"
					}
				}
			case "show_table":
				switch {
				case tableShown:
					result = "Tabellen er allerede vist til brukeren. Svar med maks én kort setning."
				case len(lastCols) > 0:
					emit(contentSSE(tableBlock(lastCols, lastRows)))
					tableShown = true
					result = "Tabellen vises nå til brukeren. Svar med maks én kort setning — ikke gjengi radene."
				default:
					result = "Ingen data å vise ennå — kjør query_database først, så show_table."
				}
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
			case "m365_search":
				var ma struct {
					Query string `json:"query"`
				}
				_ = json.Unmarshal([]byte(c.Args.String()), &ma)
				meta, _ := json.Marshal(map[string]any{"nordavind_step": "Søker i OneDrive: " + ma.Query})
				emit("data: " + string(meta))
				if uid, ok := s.m365Connected(ctx); ok {
					result = s.runM365Search(ctx, uid, ma.Query)
				} else {
					result = "Microsoft 365 er ikke koblet til."
				}
			case "m365_read":
				var ma struct {
					FileID string `json:"file_id"`
					Name   string `json:"name"`
				}
				_ = json.Unmarshal([]byte(c.Args.String()), &ma)
				step := "Leser fil"
				if ma.Name != "" {
					step = "Leser: " + ma.Name
				}
				meta, _ := json.Marshal(map[string]any{"nordavind_step": step})
				emit("data: " + string(meta))
				if uid, ok := s.m365Connected(ctx); ok {
					result = s.runM365Read(ctx, uid, ma.FileID, ma.Name)
				} else {
					result = "Microsoft 365 er ikke koblet til."
				}
			case "connect_database":
				meta, _ := json.Marshal(map[string]any{"nordavind_step": "Tester tilkoblingen"})
				emit("data: " + string(meta))
				result = s.runConnectDatabase(ctx, c.Args.String(), emit)
			case "connect_m365":
				result = s.runConnectM365(ctx, emit)
			case "check_m365":
				result = s.runCheckM365(ctx)
			case "save_m365_app":
				meta, _ := json.Marshal(map[string]any{"nordavind_step": "Lagrer app-registreringen"})
				emit("data: " + string(meta))
				result = s.runSaveM365App(ctx, c.Args.String())
			case "setup_routine":
				meta, _ := json.Marshal(map[string]any{"nordavind_step": "Setter opp rutinen"})
				emit("data: " + string(meta))
				result = s.runSetupRoutine(ctx, editID, c.Args.String())
			case "set_widget":
				meta, _ := json.Marshal(map[string]any{"nordavind_step": "Bygger widget"})
				emit("data: " + string(meta))
				result = s.runWidgetOp(ctx, widgetSlug, c.Args.String())
				if widgetSlug == "" {
					if m := widgetSlugRe.FindStringSubmatch(result); m != nil {
						createdWidget = m[1]
					}
				}
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

// answerCharLimit: flytens MaxChars når intent-modus har satt den, ellers
// standard nødbrems-grense.
func answerCharLimit(full map[string]any) int {
	if v, ok := full[flowMaxField].(int); ok && v > 0 {
		return v
	}
	if v, ok := full[flowMaxField].(float64); ok && v > 0 {
		return int(v)
	}
	return wallCharLimit
}

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

// backstopGraceful er den siste utveien hvis alt annet gir tomt: en ærlig,
// kort feilmelding — aldri en påstand om utført arbeid.
const backstopGraceful = "Klarte ikke hente et svar nå — prøv igjen."

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
	if len([]rune(strings.TrimSpace(content))) < 3 {
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

// flowNeedsDB: flyten lover databaseverktøy i flyt-tabellen.
func flowNeedsDB(flowKey string) bool {
	f, ok := intent.Flows[flowKey]
	if !ok {
		return false
	}
	for _, t := range f.Tools {
		if t == intent.ToolQueryDatabase || t == intent.ToolShowTable {
			return true
		}
	}
	return false
}

// scrubStaleRefusals fjerner utdaterte avslags-svar («du har ikke tilgang»,
// «midlertidig feil») fra payloaden som sendes modellen når datatilgang
// faktisk finnes — historikk-ankring fikk modellen til å gjenta avslag uten
// å kalle verktøyet. Lagret chat røres ikke; kun modellens arbeidskontekst.
func scrubStaleRefusals(full map[string]any) {
	msgs, _ := full["messages"].([]any)
	if len(msgs) == 0 {
		return
	}
	kept := make([]any, 0, len(msgs))
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if ok && mm["role"] == "assistant" {
			if c, ok := mm["content"].(string); ok && len([]rune(c)) <= 160 {
				lc := strings.ToLower(c)
				if (strings.Contains(lc, "ikke tilgang til") && strings.Contains(lc, "data")) ||
					strings.Contains(lc, "midlertidig feil") ||
					strings.Contains(lc, "tillater ikke lesing") {
					continue
				}
			}
		}
		kept = append(kept, m)
	}
	full["messages"] = kept
}

// dedupeStutter fjerner umiddelbar gjentakelse av samme setning — MidModel
// stammer av og til («Den nyligste … kunde Den nyligste … kunde 22894.»).
func dedupeStutter(s string) string {
	half := len(s) / 2
	for w := half; w >= 40; w-- {
		seg := s[:w]
		rest := strings.TrimSpace(s[w:])
		if strings.HasPrefix(rest, strings.TrimSpace(seg)) {
			return seg + strings.TrimSpace(rest[len(strings.TrimSpace(seg)):])
		}
	}
	return s
}
