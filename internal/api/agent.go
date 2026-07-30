package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/search"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// affirmativeRe: brukerens korte «ja, gjør det» — lest fra BRUKERTEKST, samme
// presedens som tableIntent og deepMarkers. Aldri brukt på modellens tekst.
var affirmativeRe = regexp.MustCompile(`(?i)^(ja|jepp|ok|okei|greit|kjør|gjør det|ja takk|yes|sett i gang|kjør på)[.!, ]*(gjør det|kjør|takk)?[.!]*$`)

// prevAssistantAskedQuestion: endte forrige assistentsvar med et spørsmål?
func prevAssistantAskedQuestion(full map[string]any) bool {
	msgs, _ := full["messages"].([]any)
	for i := len(msgs) - 2; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != "assistant" {
			continue
		}
		return strings.HasSuffix(strings.TrimSpace(messageText(m["content"])), "?")
	}
	return false
}

// Modellen bestemmer selv når den trenger nettet — ingen forhåndsdommer.
// widgetSlugRe plukker slugen ut av runWidgetOp-kvitteringen.
var widgetSlugRe = regexp.MustCompile(`slug=([a-z0-9-]+)`)

var webSearchTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "web_search",
		"description": "Søk på nettet. Bruk når svaret krever fersk informasjon eller fakta om " +
			"spesifikke entiteter (personer, selskaper, produkter, steder, hendelser) du ikke er " +
			"sikker på. Gjør ett kall per entitet/tema. FANT DU IKKE SVARET: gjør MINST to nye søk " +
			"med ulike vinkler (synonymer, offisielle begreper, engelsk) FØR du melder tomt — å gi opp " +
			"etter ett søk er feil. Meld først «fant ikke» når flere vinkler faktisk er prøvd.",
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

// connectIntent er sann når meldingen handler om å koble til en datakilde.
// Styrer om connector-verktøyene (~1,8k tegn) legges ved i vanlig chat.
// Bevisst romslig: å bomme koster et verktøy modellen skulle hatt, og det
// veier tyngre enn tokenene — derfor treffer den også bekreftelser («ja, gjør
// det») rett etter at assistenten selv har brakt tilkobling på bane.
var connectIntent = matchAny(
	"koble", "kobling", "tilkobl", "connect", "database", "databasen", "sql server",
	"postgres", "mysql", "mssql", "datakilde", "integrasjon", "sharepoint",
	"onedrive", "microsoft", "m365", "office 365", "azure", "app-registrering",
	"client secret", "tilgang til data",
)

// composeIntent er sann når brukeren vil SENDE noe — mail_compose er det
// største enkeltverktøyet og trengs ikke for å lese eller søke i e-post.
var composeIntent = matchAny(
	"send", "sende", "sender", "svar på", "svare på", "videresend", "vidersend",
	"skriv til", "skriv en mail", "skriv en e-post", "mail til", "e-post til",
	"epost til", "kontakt", "gi beskjed", "varsle", "purre", "purring",
	"utkast", "compose", "cc", "vedlegg",
)

// matchAny bygger et enkelt nøkkelord-filter (samme mønster som tableIntent).
func matchAny(words ...string) func(string) bool {
	return func(text string) bool {
		lower := strings.ToLower(text)
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
}

// recentTurn er siste brukermelding pluss forrige assistentsvar. Verktøy som
// legges ved etter nøkkelord må se begge: brakte assistenten tilkobling eller
// utsending på bane, holder det at brukeren svarer «ja, gjør det».
func recentTurn(full map[string]any) string {
	msgs, _ := full["messages"].([]any)
	var b strings.Builder
	seenUser := false
	for i := len(msgs) - 1; i >= 0 && b.Len() < 4000; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}
		if role == "user" && seenUser {
			break // lenger tilbake enn forrige tur er ikke relevant
		}
		if c, ok := m["content"].(string); ok {
			b.WriteString(c)
			b.WriteString(" ")
		}
		seenUser = seenUser || role == "user"
	}
	return b.String()
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

// answerStyle er husets stemme i vanlig chat. Uten den svarer modellen bare
// på det som ble spurt om, og aldri på behovet bak — brukeren må selv vite
// hvilket oppfølgingsspørsmål som var det viktige.
//
// Tre ledd, i denne rekkefølgen, og bare når de har innhold: svaret, den ene
// detaljen som endrer beslutningen, ett konkret neste steg. Aldri fyll.
// Omskrevet til eierskap (Jacob-godkjent 2026-07-28): assistenten skal DRIVE
// arbeidet, ikke bare besvare det. Fortsatt tett — leddene hoppes over når de
// er tomme — men et analysesvar uten «hva betyr dette» og «hva nå» er et
// bremsesvar, og det var målt: korrekt korrelasjonssvar på to flate linjer.
const answerStyle = "Du eier oppgaven, ikke bare svaret. Lever svaret rett på sak i første " +
	"setning. Legg så til det ENE som faktisk endrer beslutningen (risiko, avvik, mønster, " +
	"feil premiss) HVIS du har noe ekte, og avslutt med hva du ville gjort videre — ett " +
	"konkret grep formulert som handling, aldri som et tilbud om hjelp eller et spørsmål om " +
	"lov. Hopp over ledd du ikke har innhold til, og gjenta deg aldri.\n\n"

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
	// Fokus: brukerens melding styrer hvilke tabeller som vises med kolonner
	// når basen er stor.
	dbCtx := s.dbToolContextFor(ctx, lastUserText(full))

	// Agent-oppsett: /agent-flyten settes av frontend. Vi legger inn en
	// setup-instruks og administrasjonsverktøyene. Flagget må fjernes før
	// forespørselen sendes videre til upstream.
	setup, _ := full["nordavind_agent_setup"].(bool)
	delete(full, "nordavind_agent_setup")
	// Samtale-identitet + klipp-flagg fra frontend: når historikken er kuttet
	// mot tegnbudsjettet, injiseres det rullerende sammendraget som kontekst.
	// Feltene skal aldri videre upstream.
	chatID, _ := full["nordavind_chat"].(string)
	clipped, _ := full["nordavind_clipped"].(bool)
	delete(full, "nordavind_chat")
	delete(full, "nordavind_clipped")
	// Klientvern: stol aldri på at klienten holdt budsjettet — monstermeldinger
	// i historikken klippes uansett (siste brukermelding fredes).
	trimChatHistory(full)
	if chatID != "" && clipped {
		if user, ok := ctx.Value(userKey).(store.User); ok {
			if sum, err := s.store.ChatSummary(chatID, user.ID); err == nil && strings.TrimSpace(sum) != "" {
				injectSystem(full, "Tidligere i samtalen (sammendrag — bruk som bakgrunnskontekst, "+
					"ikke som verktøydata): "+sum)
			}
		}
	}
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
	if widgetSlug == "" && (flowKey == "create_widget" || flowKey == "edit_widget") {
		injectSystem(full, s.widgetSystem(ctx))
	}
	// Designlerretet: kittets ferdige flate-typer i stedet for widget-skjemaet.
	// Modellen velger layout og fyller felt — aldri komposisjon eller farger.
	designSlug, _ := full["nordavind_design"].(string)
	delete(full, "nordavind_design")
	designKit, designDoc := s.docKit(ctx, designSlug)
	if designSlug != "" {
		injectSystem(full, s.designSystem(ctx, designKit, designDoc))
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
				if !a.Enabled {
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
		if designSlug != "" {
			// Åpent lerret: KUN design-verktøyene, uansett hva ruteren måtte
			// mene om meldingen. Tomt dokument tvinges til compose — hele
			// utkastet i ett kall; ellers må turen i det minste endre noe.
			// Koden håndhever det, ikke en formulering i prompten.
			// Design-verktøyene + kunnskapsverktøyene: modellen skal kunne
			// slå opp fakta før den skriver dem på en flate.
			full["tools"] = append(designTools(designKit), s.researchTools(ctx, dbCtx)...)
			// Minst ett verktøykall, men IKKE låst til compose: modellen skal
			// kunne slå opp fakta først og bygge etterpå. Turen avsluttes i
			// kode når dokumentet faktisk er bygget.
			full["tool_choice"] = "required"
		} else if widgetSlug != "" {
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
					// web_fact ER definisjonen «spørsmål om verden utenfor» —
					// da skal svaret komme fra nettet, ikke fra hukommelsen.
					// Uten tvang svarte modellen i blinde og ble stanset av
					// kildekontrollen etterpå, med et halvt svar som resultat.
					//
					// MEN: bar tvang gjør assistenten dum. «Så da er det ikke
					// annonsert noe for 2026 enda?» er en SLUTNING fra kilder
					// som alt står i tråden — da skal den få resonnere, ikke
					// søke i blinde og rapportere tomhet. Tilstanden avgjør:
					// hadde forrige svar kilder, er tvangen unødvendig.
					if flowKey == "web_fact" && !s.answerHasSources(ctx, chatID) {
						full["tool_choice"] = map[string]any{
							"type":     "function",
							"function": map[string]any{"name": "web_search"},
						}
					}
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
			// TOKEN: settet er ~1,8k tegn og dekker en sjelden engangshandling
			// — det legges kun ved når meldingen faktisk peker dit, ikke på
			// hver eneste melding en admin skriver.
			if user, ok := ctx.Value(userKey).(store.User); ok && user.Role == "admin" &&
				connectIntent(recentTurn(full)) {
				tools = append(tools, connectorAgentTools()...)
			}
			// M365-verktøy (filer + e-post) kun når brukeren har koblet til.
			if _, ok := s.m365Connected(ctx); ok {
				tools = append(tools, m365SearchTool, m365ReadTool, mailSearchTool, mailReadTool)
				// TOKEN: mail_compose er det største enkeltverktøyet (~1,2k
				// tegn) og trengs kun når noe skal SENDES. Søk og lesing av
				// e-post ligger fortsatt alltid inne.
				if composeIntent(recentTurn(full)) {
					tools = append(tools, mailComposeTool)
				}
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
	}

	// Verktøyavhengige regler: legges på NÅR vi vet hva turen faktisk har.
	// Uten dette ville modellen fått databaseregler uten database og
	// OneDrive-regler uten M365 — ren kontekst-kostnad på hver melding.
	if tools, ok := full["tools"].([]any); ok && len(tools) > 0 {
		var opts systemOpts
		opts.tools = true
		for _, t := range tools {
			switch toolName(t) {
			case "web_search":
				opts.search = true
			case "query_database":
				opts.db = true
				opts.table = tableIntent(lastUserText(full))
			case "m365_search", "m365_read":
				opts.m365 = true
			}
		}
		if extra := moduleRules(opts); extra != "" {
			injectSystem(full, extra)
		}
	}

	// Husets stemme: kun vanlig chat. Widget-, design- og agent-flytene har
	// egne, strengere kontrakter der prat er feil.
	//
	// AV inntil den er målt: den nye søkemotoren endret svarkvaliteten, og
	// stilen skal vurderes mot den — ikke mot slik det var før.
	if s.cfg.AnswerStyle == "on" &&
		!setup && widgetSlug == "" && designSlug == "" && !connectorMode && !planning && editID == "" {
		injectSystem(full, answerStyle)
	}

	// Vanlig chat: ingen av spesialmodusene. Kun her går den fulle
	// arbeidsnarrasjonen (narrate.go) — widget-, design-, agent- og
	// connector-flytene er rene utfører-løyper som skal være tause.
	plainChat := !setup && !connectorMode && !planning && editID == "" &&
		widgetSlug == "" && designSlug == "" &&
		flowKey != "create_widget" && flowKey != "edit_widget" &&
		flowKey != "export_excel"

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

	// Bekreftelses-fella: modellen tilbød noe («Vil du at jeg …?»), brukeren
	// sa ja — og modellen stilte samme spørsmål på nytt (målt to runder på
	// rad om strømpriser den hadde verktøy til å hente). Faktatrigger:
	// forrige assistentsvar endte med «?», brukeren svarte kort bekreftende.
	// Samme presedens som datatilgangs-overstyringen: deterministisk
	// kontekst-instruks i kode, aldri formulerings-sniffing på modellen.
	if !setup && widgetSlug == "" && !connectorMode && !planning &&
		affirmativeRe.MatchString(strings.TrimSpace(lastUserText(full))) &&
		prevAssistantAskedQuestion(full) {
		injectSystem(full, "Brukeren har nettopp BEKREFTET forslaget ditt. Ikke still nye "+
			"spørsmål og ikke be om data på nytt — UTFØR det du tilbød nå, med verktøyene "+
			"du har (websøk henter eksterne tall som priser og kurser).")
	}

	// Arbeidsnarrasjonen (narrate.go): deterministiske fremdriftssteg i kode,
	// null tokens. Kun vanlig chat — utfører-flytene skal være tause.
	narr := newNarrator(emit, plainChat, func(id string) string {
		if dbCtx == nil {
			return ""
		}
		if dc, ok := dbCtx.conns[id]; ok {
			return dc.conn.Name
		}
		return ""
	})

	narr.seed(lastUserText(full))

	// Momentan kvittering: verktøyflyter melder fra i KODE før modellen har
	// startet — vises der «Tenker»-teksten står, ikke i selve svaret.
	if ack := flowAcks[flowKey]; ack.text != "" {
		narr.say(ack.text, ack.kind)
	}

	// Én linje per tur: hvilke verktøy modellen faktisk fikk. Uten denne var
	// det umulig å skille «verktøyet mangler» fra «modellen vegrer» — vi
	// diagnostiserte hybrid-vegringen blindt.
	if ts, ok := full["tools"].([]any); ok {
		var names []string
		for _, t := range ts {
			if m, ok := t.(map[string]any); ok {
				if f, ok := m["function"].(map[string]any); ok {
					if n, ok := f["name"].(string); ok {
						names = append(names, n)
					}
				}
			}
		}
		s.log.Info("tur-verktøy", "flyt", flowKey, "verktøy", strings.Join(names, ","))
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
	// Alle databaseforsøk i turen, med typet utfall. Grunnlaget for
	// kvalitetsporten: har vi noe håndfast, eller skylder vi brukeren en
	// ærlig redegjørelse? (dbstrategy.go)
	var attempts []dbAttempt
	// Benektelsesporten kjøres høyst én gang per tur.
	denialChecked := false
	// Observasjonen koden gjorde om datagrunnlaget, levert som én setning
	// etter svaret. (insight.go)
	var pending insight
	// Kode-håndhevet søkeinnsats: «fant ikke» etter bare ett søk godtas aldri —
	// modellen sendes tilbake for flere vinkler (én gang).
	searchNudged := false
	// Kapitulasjonsvakt: «umulig» etter vellykket innsamling godtas aldri —
	// én tvungen fortsettelse på grunnlaget den HAR.
	surrenderNudged := false
	// Tallvakt: et datasvar uten ett eneste siffer, i en tur der spørringene
	// faktisk ga rader, er aldri i tråd med konklusjonsregelen — målt:
	// «ingen tydelig korrelasjon basert på de daglige salgstallene», null
	// tall. Faktatrigger (sifferfravær), aldri formuleringslesing.
	numberNudged := false
	// Designlerret: compose har kjørt denne turen, så flater som venter på en
	// spørring skal få den i én fokusert runde (dataFollowup).
	composed, dataAsked, dataFilled := false, false, false
	// Spørringer kjørt i denne turen — vern mot at modellen gjentar en tung
	// spørring som allerede timet ut.
	ranQueries := map[string]bool{}
	// Kildekontroll: alt verktøyene returnerer denne turen, ordrett — prosaen
	// måles mot dette før den vises (grounding.go).
	var toolResults []string
	lastDBResult := ""
	lastDBSQL, lastDBConn := "", ""
	// Handlings-verktøy (endrer tilstand: rutine, widget, agent, m365 …) gir
	// korte kvitteringer med vilje — de skal ALDRI utløse backstop-syntesen.
	actionTool := false
	readTools := legacyReadTools
	// Tabell-garanti: show_table-verktøyet rendrer siste databasesvar
	// deterministisk, og ba brukeren om tabell rendrer vi uansett fra første
	// svar med rader — prompt alene er ikke til å stole på her.
	wantsTable := tableIntent(lastUserText(full))
	tableShown := false
	var lastCols []string
	var lastRows [][]string
	// Hjernen lærer av utvekslingen når turen er over. Backend gjør dette
	// selv: klienten skal ikke måtte huske å be om det, og hjernen skal
	// vokse av vanlig bruk — ikke bare når noen trykker på et ikon.
	userMsg := lastUserText(full)
	lastAnswer := ""
	learnFrom := chatID
	defer func() {
		s.recordUsage(ctx, full, promptTokens, completionTokens, searches, time.Since(start))
		s.learnFromExchange(ctx, learnFrom, userMsg, lastAnswer)
	}()

	for round := 0; round <= roundCap; round++ {
		body, err := json.Marshal(full)
		if err != nil {
			return
		}
		s.logPayloadShape(round, full, len(body))
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
		streamThis := !usedTool && !(dbAttempted && !dbSucceeded) &&
			flowKey != "create_widget" && flowKey != "edit_widget" &&
			flowKey != "create_presentation" && flowKey != "export_excel" && widgetSlug == ""
		if resp.StatusCode != http.StatusOK {
			s.log.Warn("upstream-runde ikke OK", "status", resp.StatusCode)
		}
		// Kildekontroll på streamede svar (G1): grunnlaget er samtalen +
		// verktøydata; gaten holder igjen udekkede påstander til de er sjekket.
		var gate *sentenceGate
		if streamThis {
			gate = newSentenceGate(groundingBasis(full, toolResults))
		}
		calls, usage, content := s.relayRound(resp, emit, streamThis, gate)
		if content != "" {
			lastAnswer = content
		}
		// Tvunget verktøyvalg gjelder kun én runde.
		delete(full, "tool_choice")
		resp.Body.Close()
		promptTokens += usage.PromptTokens
		completionTokens += usage.CompletionTokens

		// Måling: skriver modellen egen prosa i en runde som ender i verktøykall?
		// Svaret er i praksis nei (promptene forbyr det med vilje), og derfor
		// lages fremdriftsteksten deterministisk i narrate.go. Loggen står igjen
		// så vi oppdager det hvis en modell begynner å snakke likevel.
		if len(calls) > 0 {
			if prose := strings.TrimSpace(content); prose != "" {
				var names []string
				for _, c := range calls {
					names = append(names, c.Name)
				}
				s.log.Info("modell-prosa i verktøyrunde", "runde", round,
					"tegn", len([]rune(prose)), "verktøy", strings.Join(names, ","))
			}
		}

		if len(calls) == 0 {
			trimmed := strings.TrimSpace(content)
			if !searchNudged && searches == 1 && round < roundCap &&
				(gaveUpRe.MatchString(trimmed) || len([]rune(trimmed)) < 3) {
				searchNudged = true
				s.log.Info("søke-nudge utløst")
				msgs, _ := full["messages"].([]any)
				full["messages"] = append(msgs,
					map[string]any{"role": "assistant", "content": trimmed},
					map[string]any{"role": "user", "content": "Ikke gi deg etter ett søk: gjør minst to nye søk med helt andre vinkler (offisielle begreper, synonymer, engelsk) og svar deretter."})
				// Tving et faktisk søk i neste runde — instruks alene ble ignorert.
				full["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "web_search"}}
				step, _ := json.Marshal(map[string]any{"nordavind_step": "Søker med nye vinkler"})
				emit("data: " + string(step))
				continue
			}
			// Benektelsesporten: nekter svaret at noe finnes, skal vi ha lett
			// etter det først. Modellen henter gjerne topp-N, ser ikke navnet
			// i lista og konkluderer «finnes ikke» — uten at én eneste
			// spørring lette etter navnet. Ett oppslag, og bare når svaret
			// faktisk er avvisende. (dbstrategy.go)
			// Porten får sin EGEN runde: modellen har som regel brukt opp
			// budsjettet nettopp fordi den lette forgjeves, og da ville
			// «round < roundCap» stengt den ute akkurat når den trengs.
			if !denialChecked && dbCtx != nil && trimmed != "" {
				denialChecked = true
				if nudge := s.checkDenial(ctx, dbCtx, lastUserText(full), trimmed, attempts); nudge != "" {
					s.log.Info("benektelsesport: navn funnet likevel")
					roundCap = round + 1
					narr.say("Vent, det navnet finnes nesten helt sikkert. Ser en gang til.", kindDB)
					msgs, _ := full["messages"].([]any)
					full["messages"] = append(msgs,
						map[string]any{"role": "assistant", "content": trimmed},
						map[string]any{"role": "user", "content": nudge})
					continue
				}
			}

			// Kvalitetsporten: databasen ble forsøkt, men turen ga aldri én
			// eneste rad. Da finnes det ingen tall å gjengi, og modellens
			// tekst kan være fabrikkert. I stedet for den gamle faste
			// «svarte ikke i tide» (som oftest var usant — basen svarte
			// kjapt, på noe vi ikke hadde tilgang til) bygger vi en ærlig
			// redegjørelse av hva som FAKTISK skjedde, hva vi har, og hva vi
			// trenger fra brukeren. Se dbstrategy.go.
			if dbAttempted && !dbSucceeded {
				emit(contentSSE(explainAttempts(attempts, availableTables(dbCtx))))
				emit("data: [DONE]")
				return
			}
			// Subjektintegritet: andre spørringer kan ha lyktes, men hvis alle
			// som handlet om det brukeren FAKTISK spurte om feilet, har vi
			// ingenting å si om det — og skal si nettopp det. (dbstrategy.go)
			if subject, bad := subjectFailed(lastUserText(full), attempts); bad {
				s.log.Warn("subjektintegritet: alle subjekt-spørringer feilet", "subjekt", subject)
				emit(contentSSE(explainSubjectFailure(subject)))
				emit("data: [DONE]")
				return
			}
			if streamThis {
				// Slipp en ren hale, eller avgjør skjebnen til det tilbakeholdte:
				// dommer godkjenner (beregning/telling) → vis; underkjenner →
				// ett fortsettelses-omforsøk, ellers ærlig klipp. Det viste
				// prefikset er alltid kildefast og røres aldri.
				if rel := gate.finish(); rel != "" {
					emit(contentSSE(rel))
				}
				if gate.held {
					s.log.Warn("kildekontroll (stream): udekkede verdier", "avvik", strings.Join(gate.offenders, ", "))
					step, _ := json.Marshal(map[string]any{"nordavind_step": "Dobbeltsjekker mot kildene"})
					emit("data: " + string(step))
					basis := groundingBasis(full, toolResults)
					if ok, problems := s.judgeClaims(ctx, lastUserText(full), trimmed, gate.offenders, basis); ok {
						emit(contentSSE(gate.heldText()))
						narr.sourceNote(emit, trimmed, usedSources(attempts), dbCtx != nil && len(dbCtx.conns) > 1)
					} else {
						s.log.Warn("faktadommer: dikting stanset", "problemer", strings.Join(problems, "; "))
						// Midt i en setning: en modell-fortsettelse leser dårlig
						// («Du er født Jeg vet ikke …») — ta den ærlige broen i
						// kode direkte. Ved hel setningsgrense får modellen ett
						// forsøk på en kildefast fortsettelse.
						shown := gate.shownText()
						if midSentence(shown) {
							emit(contentSSE(honestCut(shown)))
						} else if cont := s.regroundContinuation(ctx, full, trimmed, shown, gate.offenders, basis, &promptTokens, &completionTokens); cont != "" {
							emit(contentSSE(cont))
							narr.sourceNote(emit, cont, usedSources(attempts), dbCtx != nil && len(dbCtx.conns) > 1)
						} else {
							emit(contentSSE(honestCut(shown)))
						}
					}
					emit("data: [DONE]")
					return
				}
				// Kun tomhets-backstop og grense-LOGG igjen (aldri omskriving
				// av noe brukeren har sett).
				switch {
				case (len([]rune(trimmed)) < 3 || isJunkAnswer(trimmed)) && usedTool && !actionTool:
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
			default:
				final := stripForeignHead(content)
				// Kapitulasjonsvakten FØR kildekontrollen: svargrenene under er
				// ikke gjensidig utelukkende (målt at vakten aldri traff da den
				// lå i én av dem). Erklærer modellen oppgaven umulig ETTER at
				// databasen og kildene faktisk leverte, er kravet som «mangler»
				// nesten alltid dens eget påfunn. Én tvungen fortsettelse.
				// Egen runde, som benektelsesporten: kapitulasjonen kommer
				// nesten alltid i SISTE runde (modellen brukte budsjettet på
				// leting), så «round < roundCap» sperret vakten permanent —
				// målt 0 treff på 15 kjøringer før dette.
				if !surrenderNudged && impossibleRe.MatchString(final) &&
					(dbSucceeded || len(toolResults) > 0) {
					surrenderNudged = true
					// To runder: én til ny spørring, én til svaret. Storm-
					// eskalering ble prøvd her og FJERNET (2026-07-28):
					// qwen3.5 brukte 3m42s per tur i prod og leverte tomt.
					// Modellen var aldri problemet — hullene våre var det.
					roundCap = round + 2
					s.log.Info("kapitulasjonsvakt utløst")
					narr.say("Nei, vent — jeg har jo tall. Bruker det jeg har.", kindDB)
					msgs, _ := full["messages"].([]any)
					full["messages"] = append(msgs,
						map[string]any{"role": "assistant", "content": final},
						map[string]any{"role": "user", "content": "Du har allerede hentet interne tall og kilder. Ikke krev mer presisjon enn spørsmålet trenger — gjør analysen med det du HAR (månedstall holder når dagstall mangler), og konkluder. Kun hvis et KONKRET tall faktisk mangler: navngi det."})
					continue
				}
				// Årstall er ikke tallfesting: «ingen korrelasjon i 2025» har
				// sifre, men null substans. Fjern årstallene før sjekken.
				if !numberNudged && dbSucceeded &&
					!strings.ContainsAny(yearRe.ReplaceAllString(final, ""), "0123456789") &&
					len([]rune(final)) > 40 {
					numberNudged = true
					roundCap = round + 1
					s.log.Info("tallvakt utløst")
					narr.say("Det der skal tallfestes. En runde til.", kindDB)
					msgs, _ := full["messages"].([]any)
					full["messages"] = append(msgs,
						map[string]any{"role": "assistant", "content": final},
						map[string]any{"role": "user", "content": "Konklusjonen din mangler tall. Skriv den på nytt og TALLFEST med tallene du allerede har hentet — ved en sammenligning eller korrelasjon skal BEGGE sidene ha tall."})
					continue
				}
				basis := groundingBasis(full, toolResults)
				if off := groundingOffenders(final, basis); len(off) > 0 {
					s.log.Warn("kildekontroll: avvik i svar", "avvik", strings.Join(off, ", "))
					// Rene tallavvik KAN være lovlige beregninger (differanser,
					// prosent) — men faktadommeren avgjør (G2); godkjent slipper
					// gjennom med kildetabellen vedlagt som kvittering. Diktede
					// NAVN blokkeres fortsatt hardt uten dommer.
					if !hasNameOffender(off) {
						if ok, problems := s.judgeClaims(ctx, lastUserText(full), final, off, basis); ok {
							emit(contentSSE(final))
							narr.deliverInsight(emit, pending, final, answerLimit, tableShown)
							narr.sourceNote(emit, final, usedSources(attempts), dbCtx != nil && len(dbCtx.conns) > 1)
							narr.sourceNote(emit, final, usedSources(attempts), dbCtx != nil && len(dbCtx.conns) > 1)
							// Tabell-garantien: bare hvis den ikke alt er vist.
							if lastDBResult != "" && !tableShown {
								emit(contentSSE("\n\n```table\n" + lastDBResult + "\n```"))
								tableShown = true
							}
							// Kode-garantiene gjelder også her: eksportkort og
							// widget-blokk skal aldri forsvinne fordi svaret tok
							// godkjent-stien.
							if flowKey == "export_excel" && lastDBResult != "" && !strings.Contains(final, "```export") {
								emit(contentSSE("\n\n```export\n" + lastDBResult + "\n```"))
							}
							if createdWidget != "" && !strings.Contains(final, "```widget") {
								emit(contentSSE("\n\n```widget\n" + createdWidget + "\n```"))
							}
							emit("data: [DONE]")
							return
						} else {
							s.log.Warn("faktadommer: tallavvik underkjent", "problemer", strings.Join(problems, "; "))
						}
					}
					step, _ := json.Marshal(map[string]any{"nordavind_step": "Dobbeltsjekker mot kildene"})
					emit("data: " + string(step))
					final = s.regroundAnswer(ctx, full, final, off, basis, &promptTokens, &completionTokens)
					if final == "" {
						// Omforsøket holdt heller ikke: vis kilden i stedet for
						// utrygg prosa — tabellen for data, kildehenvisning ellers.
						if lastDBResult != "" {
							if !tableShown {
								emit(contentSSE("Her er de faktiske dataene:\n```table\n" + lastDBResult + "\n```"))
								tableShown = true
							}
							narr.deliverInsight(emit, pending, "", answerLimit, tableShown)
							narr.sourceNote(emit, "", usedSources(attempts), dbCtx != nil && len(dbCtx.conns) > 1)
						} else {
							emit(contentSSE("Jeg fikk ikke formulert dette kildefast — se kildene over for detaljene."))
						}
						if flowKey == "export_excel" && lastDBResult != "" {
							emit(contentSSE("\n\n```export\n" + lastDBResult + "\n```"))
						}
						emit("data: [DONE]")
						return
					}
				}
				// Arbeids-dommer for viktige oppgaver: dobbeltsjekk svaret mot
				// verktøydataene FØR det vises — med egen status i chatten.
				if usedTool && importantWorkRe.MatchString(lastUserText(full)) {
					step, _ := json.Marshal(map[string]any{"nordavind_step": "Dette høres viktig ut, la meg dobbeltsjekke arbeidet mitt"})
					emit("data: " + string(step))
					if ok, problems := s.verifyWork(ctx, lastUserText(full), final, toolResults); !ok && len(problems) > 0 {
						s.log.Warn("arbeids-dommer: underkjent", "problemer", strings.Join(problems, "; "))
						if fixed := s.regroundAnswer(ctx, full, final, problems, toolResults, &promptTokens, &completionTokens); fixed != "" {
							final = fixed
						}
					}
				}
				// Innsats-sjekk ETTER kildekontrollen: også et forsiktig
				// omforsøk som melder tomt etter få søk sendes tilbake.
				if !searchNudged && searches <= 2 && round < roundCap && gaveUpRe.MatchString(final) {
					searchNudged = true
					s.log.Info("søke-nudge utløst", "searches", searches)
					msgs, _ := full["messages"].([]any)
					full["messages"] = append(msgs,
						map[string]any{"role": "assistant", "content": final},
						map[string]any{"role": "user", "content": "Ikke gi deg ennå: gjør minst to nye søk med helt andre vinkler (offisielle begreper, synonymer, engelsk) og svar deretter."})
					full["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "web_search"}}
					step, _ := json.Marshal(map[string]any{"nordavind_step": "Søker med nye vinkler"})
					emit("data: " + string(step))
					continue
				}
				emit(contentSSE(final))
				narr.deliverInsight(emit, pending, final, answerLimit, tableShown)
			}
			// Eksportflyt: eksportkortet leveres ALLTID i kode — brukeren ba
			// om eksport, da skal valget stå der uten oppfølgingsspørsmål.
			if flowKey == "export_excel" && lastDBResult != "" && !strings.Contains(content, "```export") {
				emit(contentSSE("\n\n```export\n" + lastDBResult + "\n```"))
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
			if c.Name == "mail_compose" {
				var ma struct {
					ToEmail    string `json:"to_email"`
					ToName     string `json:"to_name"`
					Subject    string `json:"subject"`
					Body       string `json:"body"`
					AttachSQL  string `json:"attach_sql"`
					AttachConn string `json:"attach_connection_id"`
					AttachName string `json:"attach_filename"`
				}
				_ = json.Unmarshal([]byte(c.Args.String()), &ma)
				spec := map[string]any{
					"to":      []map[string]string{},
					"subject": ma.Subject,
					"body":    ma.Body,
				}
				if strings.TrimSpace(ma.ToEmail) != "" {
					spec["to"] = []map[string]string{{"name": ma.ToName, "address": ma.ToEmail}}
				}
				// Kjørte modellen spørringen FØR kortet (vanlig mønster) og
				// brukeren ba om vedlegg/oversikt: fest samme spørring
				// deterministisk i stedet for å kreve attach_sql-disiplin.
				if strings.TrimSpace(ma.AttachSQL) == "" && lastDBSQL != "" &&
					attachHintRe.MatchString(lastUserText(full)) {
					ma.AttachSQL, ma.AttachConn = lastDBSQL, lastDBConn
				}
				if strings.TrimSpace(ma.AttachSQL) != "" {
					if user, ok := ctx.Value(userKey).(store.User); ok {
						meta, _ := json.Marshal(map[string]any{"nordavind_step": "Bygger Excel-vedlegget"})
						emit("data: " + string(meta))
						id, name, nrows, errMsg := s.buildQueryAttachment(ctx, user.ID, dbCtx, ma.AttachConn, ma.AttachSQL, ma.AttachName)
						if errMsg != "" {
							// Vedlegget feilet: ærlig beskjed + kort uten vedlegg.
							emit(contentSSE("Fikk ikke laget Excel-vedlegget (" + errMsg + ") — kortet under er uten vedlegg.\n\n"))
						} else {
							spec["attachments"] = []map[string]any{{"id": id, "name": name, "rows": nrows}}
						}
					}
				}
				sj, _ := json.Marshal(spec)
				emit(contentSSE("```mailcompose\n" + string(sj) + "\n```"))
				emit("data: [DONE]")
				return
			}
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
				narr.before(c.Name, c.Args.String())
				stopWait := narr.slow(c.Name)
				// Samme spørring to ganger i én tur er alltid feil: enten
				// virket den (og svaret ligger i historikken), eller så timet
				// den ut — og da koster gjentakelsen 30 sekunder til.
				key := args.ConnectionID + "|" + strings.Join(strings.Fields(args.SQL), " ")
				if ranQueries[key] {
					result = "Du har allerede kjørt nøyaktig denne spørringen i denne turen. " +
						"Skriv en ANNEN spørring (lettere eller mot andre kolonner), " +
						"eller svar brukeren med det du har."
				} else {
					ranQueries[key] = true
					att := s.runDBQuery(ctx, dbCtx, args.ConnectionID, args.SQL)
					result = att.text
					// Hele turens forsøkshistorikk: kvalitetsporten bruker den
					// til å avgjøre om vi faktisk har noe håndfast å svare med.
					attempts = append(attempts, att)
					if att.ok() {
						lastDBResult = att.text
						lastDBSQL, lastDBConn = args.SQL, args.ConnectionID
						lastCols, lastRows = att.cols, att.rows
						// Observer resultatet mens radene er ferske. Siste
						// vellykkede spørring vinner — det er den svaret
						// bygger på. (insight.go)
						// pending SKAL alltid speile siste vellykkede spørring,
						// også når den ikke ga noen observasjon. Uten
						// nullstillingen overlevde en observasjon fra en
						// utforskende spørring tidligere i turen og ble limt på
						// et svar om noe helt annet («Dataene stopper 1. juni»
						// på «margindata finnes ikke»).
						o, ok := observe(att.cols, att.rows, args.SQL,
							lastUserText(full), connector.MaxRows, time.Now())
						if ok {
							pending = o
						} else {
							pending = insight{}
						}
					}
				}
				stopWait()
				narr.afterDB(lastAttempt(attempts), result)
				dbAttempted = true
				dbSucceeded = dbSucceeded || anyOK(attempts)
				// Send spørringen som metadata så tabellen i svaret kan tilby
				// live Excel-kobling (frontend fester den til meldingen).
				if strings.TrimSpace(args.SQL) != "" {
					qm, _ := json.Marshal(map[string]any{"nordavind_query": map[string]string{
						"connection_id": args.ConnectionID,
						"sql":           args.SQL,
					}})
					emit("data: " + string(qm))
				}
				// Rendr tabellen deterministisk med en gang når brukeren ba om
				// den. Radene tas fra att, ikke fra en ny JSON-parsing av
				// result — det siste kjørte også på ikke-JSON svar som
				// «du har allerede kjørt denne spørringen».
				if wantsTable && !tableShown && len(lastRows) > 0 {
					emit(contentSSE(tableBlock(lastCols, lastRows)))
					tableShown = true
					result += "\n\n(Tabellen er allerede vist til brukeren — IKKE gjengi radene i tekst, legg til maks én kort setning.)"
				}
			case "show_table":
				narr.before(c.Name, c.Args.String())
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
				narr.before(c.Name, c.Args.String())
				stopWait := narr.slow(c.Name)
				result = s.runFetchURL(ctx, args.URL)
				stopWait()
				narr.after(c.Name, result)
			case "create_agent", "update_agent", "delete_agent", "list_agents":
				var aa agentToolArgs
				_ = json.Unmarshal([]byte(c.Args.String()), &aa)
				narr.before(c.Name, c.Args.String())
				switch c.Name {
				case "create_agent":
					result = s.runCreateAgent(ctx, aa)
				case "update_agent":
					result = s.runUpdateAgent(ctx, aa)
				case "delete_agent":
					result = s.runDeleteAgent(ctx, aa)
				default:
					result = s.runListAgents(ctx)
				}
			case "mail_search":
				narr.before(c.Name, c.Args.String())
				stopWait := narr.slow(c.Name)
				if uid, ok := s.m365Connected(ctx); ok {
					result = s.runMailSearch(ctx, uid, args.Query)
				} else {
					result = "Microsoft 365 er ikke koblet til."
				}
				stopWait()
				narr.after(c.Name, result)
			case "mail_read":
				narr.before(c.Name, c.Args.String())
				var ma struct {
					MailID string `json:"mail_id"`
				}
				json.Unmarshal([]byte(c.Args.String()), &ma)
				if uid, ok := s.m365Connected(ctx); ok {
					result = s.runMailRead(ctx, uid, ma.MailID)
				} else {
					result = "Microsoft 365 er ikke koblet til."
				}
				narr.after(c.Name, result)
			case "m365_search":
				var ma struct {
					Query string `json:"query"`
				}
				_ = json.Unmarshal([]byte(c.Args.String()), &ma)
				narr.before(c.Name, c.Args.String())
				stopWait := narr.slow(c.Name)
				if uid, ok := s.m365Connected(ctx); ok {
					result = s.runM365Search(ctx, uid, ma.Query)
				} else {
					result = "Microsoft 365 er ikke koblet til."
				}
				stopWait()
				narr.after(c.Name, result)
			case "m365_read":
				var ma struct {
					FileID string `json:"file_id"`
					Name   string `json:"name"`
				}
				_ = json.Unmarshal([]byte(c.Args.String()), &ma)
				narr.before(c.Name, c.Args.String())
				stopWait := narr.slow(c.Name)
				if uid, ok := s.m365Connected(ctx); ok {
					result = s.runM365Read(ctx, uid, ma.FileID, ma.Name)
				} else {
					result = "Microsoft 365 er ikke koblet til."
				}
				stopWait()
				narr.after(c.Name, result)
			case "connect_database":
				narr.before(c.Name, c.Args.String())
				stopWait := narr.slow(c.Name)
				result = s.runConnectDatabase(ctx, c.Args.String(), emit)
				stopWait()
				narr.after(c.Name, result)
			case "connect_m365":
				result = s.runConnectM365(ctx, emit)
			case "check_m365":
				result = s.runCheckM365(ctx)
			case "save_m365_app":
				narr.before(c.Name, c.Args.String())
				result = s.runSaveM365App(ctx, c.Args.String())
			case "setup_routine":
				narr.before(c.Name, c.Args.String())
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
			case "compose", "patch", "restyle":
				// Designlerretet: hver operasjon lagres med en gang, og
				// lerretet får beskjed om å hente dokumentet på nytt.
				step := map[string]string{"compose": "Bygger dokumentet",
					"patch": "Endrer flaten", "restyle": "Bytter uttrykk"}[c.Name]
				meta, _ := json.Marshal(map[string]any{"nordavind_step": step})
				emit("data: " + string(meta))
				switch c.Name {
				case "compose":
					result = s.runCompose(ctx, designSlug, c.Args.String())
					composed = true
					// compose ER hele dokumentet: et nytt kall ville bare bygd
					// det samme på nytt (modellen gjorde det tre ganger og
					// tredoblet regningen). Koden stenger døren.
					full["tool_choice"] = "none"
				case "patch":
					result = s.runDesignPatch(ctx, designSlug, c.Args.String(), dataAsked)
					// Ett patch-kall holder: verktøyet tar alle endringene i
					// ops. Uten denne sperren gjentok modellen samme endring
					// flere ganger og la inn duplikater.
					dataFilled = true
					full["tool_choice"] = "none"
				default:
					result = s.runRestyle(ctx, designSlug, c.Args.String())
				}
				dm, _ := json.Marshal(map[string]any{"nordavind_design_updated": designSlug})
				emit("data: " + string(dm))
			default:
				narr.before("web_search", c.Args.String())
				searches++
				var sources []sourceRef
				stopWait := narr.slow("web_search")
				result, sources = s.runWebSearchFor(ctx, args.Query, recentTurn(full))
				stopWait()
				// Kildeantallet er det ekte funnet her, ikke tekstlengden.
				narr.afterHits("web_search", len(sources))
				if len(sources) > 0 {
					// Kildene sendes som metadata til frontend — de skal ikke stå i svaret.
					meta, _ := json.Marshal(map[string]any{"nordavind_sources": sources})
					emit("data: " + string(meta))
				}
			}
			if readTools[c.Name] {
				toolResults = append(toolResults, result)
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
		// TOKEN: uten dette vokser turen kvadratisk — hver runde re-sender
		// ALLE tidligere verktøyresultater ubeskåret (grundig-modus endte på
		// 30-50k prompt-tokens per svar). Eldste resultater klippes først;
		// kildekontrollen mister ingenting, den måler mot toolResults-lista.
		trimToolHistory(full, toolHistoryBudget)

		// Lerretet ER svaret: er dokumentet bygget og ingen flater venter på
		// tall, avslutter koden turen her. Å sende hele historikken opp igjen
		// bare for å få et «Ok» tilbake er en hel runde betalt for ingenting.
		if (composed || dataFilled) && designSlug != "" {
			if ask, need := s.dataFollowup(ctx, designSlug); need && !dataAsked {
				dataAsked = true
				msgs, _ := full["messages"].([]any)
				full["messages"] = append(msgs, map[string]any{"role": "user", "content": ask})
				full["tool_choice"] = map[string]any{
					"type": "function", "function": map[string]any{"name": "patch"}}
				step, _ := json.Marshal(map[string]any{"nordavind_step": "Kobler på tallene"})
				emit("data: " + string(step))
				composed = false
				continue
			}
			emit("data: [DONE]")
			return
		}

		// Tvungen verktøybruk gjelder KUN første runde (presentasjonsflyten):
		// minst én endring skal skje, men modellen skal kunne stoppe etterpå.
		// «none» settes derimot bevisst på siste runde og skal stå.
		if tc, ok := full["tool_choice"]; ok && tc != "none" {
			delete(full, "tool_choice")
		}

		if round == roundCap-1 {
			// Siste runde: tving frem et svar uten flere verktøykall, og be
			// modellen tydelig om å svare NÅ fra det den har funnet — ikke tomt.
			injectSystem(full, backstopNudge)
			// TOKEN: uten verktøybruk i turen fjernes katalogen (~3k tokens).
			// HAR turen brukt verktøy, må den bli stående — se disableTools.
			disableTools(full)
		}
	}
	// Løkka gikk tom for runder uten et rent svar (modellen ville søke videre).
	// Synteser fra det den alt har hentet, så bruker aldri sitter igjen med blankt.
	step, _ := json.Marshal(map[string]any{"nordavind_step": "Oppsummerer funnene"})
	emit("data: " + string(step))
	s.streamBackstop(ctx, full, emit, &promptTokens, &completionTokens)
	emit("data: [DONE]")
}

// toolHistoryBudget: samlet tak (tegn) på verktøyresultater som bæres videre
// mellom rundene i én tur. Ferske resultater beholdes alltid uklippet —
// modellen bruker dem i runden rett etter kallet. Eldre resultater er som
// regel alt konsumert; de klippes til et hode når budsjettet sprenges.
const toolHistoryBudget = 20000

// toolTrimHead: hvor mye av et klippet verktøyresultat som beholdes.
const toolTrimHead = 500

// trimToolHistory holder summen av tool-meldinger i samtalen under budsjettet
// ved å klippe de ELDSTE først. Vanlige turer (1-3 verktøykall) rører den
// aldri; den finnes for grundig-modus og søketunge turer der historikken
// ellers vokser kvadratisk med rundetallet.
func trimToolHistory(full map[string]any, budget int) {
	msgs, _ := full["messages"].([]any)
	type toolRef struct {
		m    map[string]any
		size int
	}
	// Halen (rundens ferske resultater) er fredet: modellen har ikke lest dem
	// ennå — de skal alltid frem uklippet i neste kall.
	tail := len(msgs)
	for tail > 0 {
		mm, ok := msgs[tail-1].(map[string]any)
		if !ok || mm["role"] != "tool" {
			break
		}
		tail--
	}
	var refs []toolRef
	total := 0
	for _, m := range msgs[:tail] {
		mm, ok := m.(map[string]any)
		if !ok || mm["role"] != "tool" {
			continue
		}
		c, _ := mm["content"].(string)
		refs = append(refs, toolRef{mm, len(c)})
		total += len(c)
	}
	for _, r := range refs {
		if total <= budget {
			return
		}
		if r.size <= toolTrimHead {
			continue
		}
		c, _ := r.m["content"].(string)
		if ru := []rune(c); len(ru) > toolTrimHead {
			r.m["content"] = string(ru[:toolTrimHead]) +
				"\n…[forkortet for plass — innholdet er alt lest og brukt tidligere i turen]"
		}
		total -= r.size - len(r.m["content"].(string))
	}
}

// chatMsgCharCap: hardt tak per historikk-melding fra klienten. Frontends
// budsjett klipper normalt lenge før dette — taket verner mot gamle klienter
// og tredjeparter som sender hele dokumenter i hver melding.
const chatMsgCharCap = 20000

// trimChatHistory klipper enkeltmeldinger i klient-historikken over taket
// (hode + hale, midten ut). Siste brukermelding fredes alltid — det er den
// aktive turen, og vedlegg der er med vilje.
func trimChatHistory(full map[string]any) {
	msgs, _ := full["messages"].([]any)
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if mm, ok := msgs[i].(map[string]any); ok && mm["role"] == "user" {
			lastUser = i
			break
		}
	}
	for i, m := range msgs {
		if i == lastUser {
			continue
		}
		mm, ok := m.(map[string]any)
		if !ok || (mm["role"] != "user" && mm["role"] != "assistant") {
			continue
		}
		c, ok := mm["content"].(string)
		if !ok {
			continue
		}
		if r := []rune(c); len(r) > chatMsgCharCap {
			half := chatMsgCharCap / 2
			mm["content"] = string(r[:half]) +
				"\n…[midten av meldingen utelatt for plass]…\n" + string(r[len(r)-half:])
		}
	}
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
		Source:           "chat",
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

// legacyReadTools: verktøy hvis resultat legacy-løkka behandler som lest
// innhold. Hevet ut av runAgentLoop uendret, så motor v6 kan måle sitt eget
// evidens-sett mot det (motorwire_test.go) i stedet for å duplisere lista.
var legacyReadTools = map[string]bool{
	"web_search": true, "fetch_url": true, "query_database": true,
	"m365_search": true, "m365_read": true, "mail_search": true, "mail_read": true,
	"list_agents": true, "show_table": true,
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
	disableTools(full)
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
	// Også backstop-syntesen er kildekontrollert — den gjenforteller verktøydata
	// (som ligger i samtalen på dette tidspunktet) og kan dikte akkurat som
	// hovedsvaret.
	// BUFRET, ikke strømmet: backstopen kjører på samme modell som nettopp
	// degenererte, og en live-strøm har sendt søppelet («évekekek …») før
	// noen port rekker å se det. Syntesen er 2-3 setninger — bufring koster
	// millisekunder og gir formsjekk + kildekontroll på HELE svaret først.
	_, usage, content := s.relayRound(resp, func(string) {}, false, nil)
	resp.Body.Close()
	*promptTokens += usage.PromptTokens
	*completionTokens += usage.CompletionTokens
	trim := stripForeignHead(strings.TrimSpace(content))
	if len([]rune(trim)) < 3 || isJunkAnswer(trim) {
		s.log.Warn("backstop: formsjekk avviste syntesen", "tegn", len([]rune(trim)))
		emit(contentSSE(backstopGraceful))
		return
	}
	basis := groundingBasis(full, nil)
	if off := groundingOffenders(trim, basis); len(off) > 0 {
		if ok, _ := s.judgeClaims(ctx, lastUserText(full), trim, off, basis); !ok {
			s.log.Warn("faktadommer (backstop): dikting stanset", "avvik", strings.Join(off, ", "))
			emit(contentSSE(honestCut("")))
			return
		}
	}
	emit(contentSSE(trim))
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

// answerHasSources: bygde forrige assistentsvar i samtalen på websøk? Da
// ligger kildene i tråden og oppfølgingsspørsmål trenger ikke tvunget søk.
// Ukjent chat (agenter, rutiner, uinnlogget) → false, altså uendret tvang.
func (s *Server) answerHasSources(ctx context.Context, chatID string) bool {
	if chatID == "" {
		return false
	}
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return false
	}
	had, err := s.store.LastAnswerHadSources(chatID, user.ID)
	return err == nil && had
}

// runWebSearch utfører søk + sidehenting og formaterer kildekontekst.
// Utdragsmotoren (excerpt.go) velger de mest relevante avsnittene; cache-
// treff koster null oppstrøms. Ved embedding-feil: fail-open til gammel
// full-side-oppførsel — riktig svar er pri 1.
func (s *Server) runWebSearch(ctx context.Context, query string) (string, []sourceRef) {
	return s.runWebSearchFor(ctx, query, "")
}

// runWebSearchFor er søket med brukerens FAKTISKE spørsmål tilgjengelig.
//
// Søkestrengen modellen skriver er en forespørsel til søkemotoren, ikke et
// uttrykk for hva brukeren lurer på: «Hva var ordet i 2025?» i en samtale om
// Norge ble til søket «Årets ord 2025», og da rangerte danske avsnitt like
// høyt som norske. Relevans måles derfor mot spørsmålet OG søkestrengen —
// konteksten vi allerede har, brukt der valget faktisk tas. Tom intent
// (rutiner, planlagte agenter) oppfører seg nøyaktig som før.
func (s *Server) runWebSearchFor(ctx context.Context, query, intentText string) (string, []sourceRef) {
	if strings.TrimSpace(query) == "" {
		return "Tomt søk.", nil
	}
	// Cachenøkkelen må bære begge: samme søkestreng i to ulike samtaler kan
	// fortjene ulike utdrag.
	cacheKey := query
	if intentText != "" {
		cacheKey = query + "\x00" + intentText
	}
	if cached, refs, ok := searchCacheGet(cacheKey); ok {
		s.log.Info("websøk", "query", query, "cache", true)
		return cached, refs
	}
	results, err := s.search.Search(ctx, query)
	if err != nil {
		s.log.Warn("websøk feilet", "query", query, "err", err)
	}
	// Wikipedia som ekstra kilde ved siden av motortreffene. Prod-IP-en er
	// blokkert av nesten alle motorer (målt: kun mojeek svarer, og den
	// rate-limiter etter ~10 søk), så uten dette står turen ofte helt uten
	// kilder. Wikipedia blokkerer oss ikke. Irrelevante artikler rangeres
	// bort av utdragsmotoren — derfor ingen «passer dette?»-detektor.
	results = mergeSources(results, s.search.WikiSearch(ctx, query))
	if len(results) == 0 {
		s.log.Warn("websøk uten treff", "query", query)
		return "Søket ga ingen resultater.", nil
	}
	// Sosiale medier bakerst FØR kuttet — ellers presser et YouTube/Reddit-
	// treff en faktakilde ut av topp-N (Aurora-fødselsdato-saken).
	results = demoteSocial(results)
	if len(results) > excerptPages {
		results = results[:excerptPages]
	}
	// Alltid full sidehenting: snippet-søket ga feilkoblinger (24AI-saken) —
	// korrekthet vinner over fart, Jacobs beslutning 2026-07-26.
	pages := s.search.FetchPages(ctx, results, excerptChars)

	refs := make([]sourceRef, 0, len(results))
	for _, r := range results {
		refs = append(refs, sourceRef{Title: r.Title, URL: r.URL})
	}

	// Relevans-vektoren: brukerens spørsmål veier tyngst, søkestrengen bærer
	// begrepene som faktisk står på sidene. Sammen fanger de både HVA det
	// spørres om og HVILKEN sammenheng det står i.
	relevance := query
	if intentText != "" {
		relevance = intentText + "\n" + query
	}
	var context string
	queryVec, qErr := s.embedCached(ctx, relevance)
	if qErr == nil {
		if picked, rErr := rankExcerpts(ctx, s.embedBatch, queryVec, pages); rErr == nil && len(picked) > 0 {
			context = formatExcerptContext(query, results, picked)
		}
	}
	if context == "" {
		// Fail-open: rangeringen feilet — send sidene som før (trunkert).
		for i, p := range pages {
			if r := []rune(p); len(r) > 2800 {
				pages[i] = string(r[:2800])
			}
		}
		if len(results) > 3 {
			results, pages, refs = results[:3], pages[:3], refs[:3]
		}
		context = search.FormatContext(query, results, pages)
	}
	context += "\nTrenger du mer enn dette: kall fetch_url på den mest relevante kilden."
	s.log.Info("websøk", "query", query, "treff", len(results), "tegn", len(context))
	searchCachePut(cacheKey, context, refs)
	return context, refs
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

// flowAcks: momentane kvitteringer per verktøyflyt — sendes av koden før
// modellen starter, så selv trege spørringer FØLES umiddelbare.
// flowAcks er kvitteringen som går på t=0, før modellen har rukket å velge
// verktøy. Den skal FORBEREDE, ikke gjenta det neste steg sier: «Sjekker
// tallene nå.» rett før «Spør Kunder om orders.» var to setninger om samme
// handling. Går gjennom narratoren, så dedupen og turtelleren eier den.
var flowAcks = map[string]struct{ text, kind string }{
	"data_question":  {"Ett øyeblikk, jeg går til tallene.", kindDB},
	"show_table":     {"Ett øyeblikk.", kindTable},
	"web_fact":       {"Ett øyeblikk, jeg slår opp.", kindWeb},
	"m365_files":     {"Ett øyeblikk, jeg ser i filene dine.", kindFile},
	"create_routine": {"Ett øyeblikk, jeg rigger dette.", kindAgent},
	"edit_routine":   {"Ett øyeblikk, jeg justerer.", kindAgent},
}

// emitAfter sender en steg-status hvis operasjonen fortsatt pågår etter d.
// Returnert funksjon stanser varselet (kalles når operasjonen er ferdig).
func emitAfter(emit func(string), d time.Duration, status string) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(d):
			meta, _ := json.Marshal(map[string]any{"nordavind_step": status})
			emit("data: " + string(meta))
		}
	}()
	return func() { close(done) }
}

// gaveUpRe: svar som melder tomt uten reell innsats.
var yearRe = regexp.MustCompile(`\b(19|20)\d\d\b`)

var gaveUpRe = regexp.MustCompile(`(?i)fant (ikke|ingen)|ingen pålitelig|ikke pålitelig informasjon`)

// impossibleRe: modellen erklærer oppgaven umulig. Samme kontraktsklasse som
// gaveUpRe (driver et OMFORSØK, aldri en tillitsdom — en falsk positiv koster
// én ekstra runde, ikke et galt svar). Målt: «ingen tilgjengelige daglige
// oljeprisdata … korrelasjonsanalyse kan ikke gjennomføres» — etter fire
// vellykkede kilder og ferske salgstall; kravet om DAGLIGE tall var dens eget.
var impossibleRe = regexp.MustCompile(`(?i)kan ikke (gjennomføres|beregnes|utføres|analyseres)` +
	`|(kan|klarer) (jeg |vi )?(dessverre )?ikke (å )?(regne|beregne|gjennomføre|utføre|analysere|fastslå|sammenligne)` +
	`|ikke mulig å|finnes ingen (åpent )?tilgjengelige|analysen? (er|blir) ikke mulig`)

// attachHintRe: brukeren ba om fil/vedlegg/oversikt i e-posten.
var attachHintRe = regexp.MustCompile(`(?i)vedlegg|excel|xlsx|regneark|oversikt|rapport|liste|fil\b`)
