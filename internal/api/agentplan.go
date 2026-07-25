package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

const (
	// Spinup er den dyre engangsjobben: agenten får god tid og mange runder på
	// å finne ut HVORDAN oppgaven løses best, og skal ende med en plan som
	// faktisk er prøvekjørt.
	planMaxRounds  = 14
	planMaxSteps   = 8
	planTimeout    = 10 * time.Minute
	planStepChars  = 4000 // maks tegn vi tar med fra ett steg-resultat
	planReportChar = 6000 // maks tegn med steg-data inn i tolkningskallet
)

// agentStep er ett utført steg i en kompilert plan. Kind avgjør hvilke felt
// som brukes: sql (connection_id + sql), web (query), fetch (url).
type agentStep struct {
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	ConnectionID string `json:"connection_id,omitempty"`
	SQL          string `json:"sql,omitempty"`
	Query        string `json:"query,omitempty"`
	URL          string `json:"url,omitempty"`
}

// agentPlan er resultatet av spinup: de konkrete stegene som skal kjøres hver
// gang, hva som skal følges med på, og når det er verdt å si fra.
type agentPlan struct {
	Approach  string      `json:"approach"`   // kort begrunnelse for valgt fremgangsmåte
	Steps     []agentStep `json:"steps"`      // utføres i rekkefølge, deterministisk
	Watch     string      `json:"watch"`      // hva som skal vurderes i resultatet
	AlertRule string      `json:"alert_rule"` // når det er verdt å varsle brukeren
}

// upstreamMessage er ett svar fra modellen, normalisert på tvers av
// strukturerte og tekstlige verktøykall.
type upstreamMessage struct {
	visible string
	calls   []normCall
	tokens  int
}

type normCall struct{ id, name, args string }

// chatOnce gjør ett ikke-strømmende kall mot upstream og normaliserer svaret.
func (s *Server) chatOnce(ctx context.Context, model string, messages []any, tools []any, maxTokens int) (upstreamMessage, error) {
	var m upstreamMessage
	payload := map[string]any{
		"model":       model,
		"messages":    messages,
		"stream":      false,
		"temperature": 0.2,
		"max_tokens":  maxTokens,
		"reasoning":   map[string]any{"enabled": false},
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	body, _ := json.Marshal(payload)
	req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
	if err != nil {
		return m, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return m, err
	}
	m.tokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
	if len(out.Choices) == 0 {
		return m, fmt.Errorf("tomt svar fra modellen")
	}
	msg := out.Choices[0].Message
	for _, c := range msg.ToolCalls {
		m.calls = append(m.calls, normCall{c.ID, c.Function.Name, c.Function.Arguments})
	}
	visible, _, blocks := splitToolCallText(msg.Content)
	for i, b := range blocks {
		if name, args := parseTextualToolCall(b); name != "" {
			m.calls = append(m.calls, normCall{fmt.Sprintf("call_t%d", i), name, args})
		}
	}
	m.visible = strings.TrimSpace(visible)
	return m, nil
}

// savePlanTool er verktøyet spinup avslutter med. Stegene valideres server-side
// før planen godtas, så en plan som lagres er en plan som faktisk kjører.
var savePlanTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "save_plan",
		"description": "Lagre den ferdige planen. Kall dette KUN når du har prøvekjørt hvert steg og " +
			"vet at det gir riktige data. Stegene kjøres akkurat som oppgitt ved hver kjøring.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"approach": map[string]any{
					"type":        "string",
					"description": "én til to setninger om hvorfor denne fremgangsmåten er den beste",
				},
				"steps": map[string]any{
					"type":        "array",
					"description": "stegene som skal kjøres hver gang, i rekkefølge (maks 8)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind":          map[string]any{"type": "string", "description": "sql | web | fetch"},
							"label":         map[string]any{"type": "string", "description": "kort beskrivelse av hva steget henter"},
							"connection_id": map[string]any{"type": "string", "description": "kun for kind=sql"},
							"sql":           map[string]any{"type": "string", "description": "kun for kind=sql, ferdig SELECT"},
							"query":         map[string]any{"type": "string", "description": "kun for kind=web, søkefrasen"},
							"url":           map[string]any{"type": "string", "description": "kun for kind=fetch, konkret URL"},
						},
						"required": []string{"kind", "label"},
					},
				},
				"watch": map[string]any{
					"type":        "string",
					"description": "hva som skal vurderes i dataene hver kjøring, konkret",
				},
				"alert_rule": map[string]any{
					"type":        "string",
					"description": "når resultatet er verdt å varsle om, med terskler der det gir mening",
				},
			},
			"required": []string{"steps", "watch", "alert_rule"},
		},
	},
}

const planSystem = "Du er i SPINUP: du skal ikke levere svaret på oppgaven, du skal finne ut HVORDAN den " +
	"løses best og billigst, og så kompilere det til en plan som kjøres om igjen hver gang.\n" +
	"ARBEIDSMÅTE:\n" +
	"- Utforsk først. Prøv spørringer mot databasen, søk, les kilder. Du har god tid nå — dette gjøres " +
	"kun én gang, mens planen kjøres mange ganger.\n" +
	"- Verifiser at dataene du får faktisk er det oppgaven ber om. Feil kolonne eller feil filter nå " +
	"betyr feil svar hver eneste kjøring. Sjekk at tallene er rimelige før du godtar en spørring.\n" +
	"- Velg den mest effektive veien: færrest mulig steg som fortsatt gir hele bildet. Ett godt " +
	"aggregat slår fem rå-uttrekk.\n" +
	"- Spørringene skal være tidsrelative (f.eks. siste 7 dager regnet fra kjøretidspunktet), aldri " +
	"hardkodede datoer — planen skal fortsatt være riktig om et halvt år.\n" +
	"- Ikke ta med steg som gir det samme hver gang og aldri endrer seg. Planen skal hente det som " +
	"faktisk må sjekkes på nytt.\n" +
	"AVSLUTT med save_plan: stegene som skal kjøres hver gang, hva som skal vurderes (watch), og når " +
	"det er verdt å varsle brukeren (alert_rule, med konkrete terskler der det gir mening). " +
	"Server-siden prøvekjører stegene dine — får du dem i retur med feil, rett dem og lagre på nytt."

// planTools gir spinup de samme verktøyene som en kjøring, pluss save_plan.
func planTools(dbCtx *dbToolCtx) []any {
	tools := []any{webSearchTool, fetchURLTool}
	if dbCtx != nil {
		tools = append(tools, dbCtx.tool)
	}
	return append(tools, savePlanTool)
}

// startPlanBuild kjører spinup i bakgrunnen. Idempotent — samme agent bygges
// aldri to ganger samtidig.
func (s *Server) startPlanBuild(agentID string) {
	s.planMu.Lock()
	if s.planBuilding[agentID] {
		s.planMu.Unlock()
		return
	}
	s.planBuilding[agentID] = true
	s.planMu.Unlock()

	go func() {
		defer func() {
			s.planMu.Lock()
			delete(s.planBuilding, agentID)
			s.planMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), planTimeout)
		defer cancel()
		s.buildAgentPlan(ctx, agentID)
	}()
}

// buildAgentPlan er spinup: agenten utforsker oppgaven grundig én gang og
// lagrer en prøvekjørt plan som hver kjøring så følger.
func (s *Server) buildAgentPlan(ctx context.Context, agentID string) {
	a, err := s.store.MissionAgent(agentID)
	if err != nil {
		s.log.Warn("spinup: fant ikke agenten", "id", agentID, "err", err)
		return
	}
	if strings.TrimSpace(a.Task) == "" {
		return
	}
	s.store.SetPlanStatus(agentID, "building", "")
	s.saveActivity(agentID, "Finner beste fremgangsmåte …", nil)

	dbCtx := s.buildDBTool(a.TenantID, a.UserID, "")
	tools := planTools(dbCtx)

	userMsg := "Oppgaven agenten skal utføre hver kjøring:\n" + a.Task
	if a.ScheduleLabel != "" {
		userMsg += "\n\nFrekvens: " + a.ScheduleLabel
	}
	if dbCtx != nil {
		if dc, ok := dbCtx.conns[a.ConnectionID]; ok {
			userMsg += "\n\nBrukeren pekte ut databasen " + dc.conn.Name +
				" (connection_id=" + a.ConnectionID + ")."
		}
	}
	messages := []any{
		map[string]any{"role": "system", "content": planSystem},
		map[string]any{"role": "user", "content": userMsg},
	}

	var totalTokens int
	var steps []string
	for round := 0; round < planMaxRounds; round++ {
		if ctx.Err() != nil {
			s.failPlan(a, "Tidsfristen for planleggingen løp ut.", totalTokens)
			return
		}
		msg, err := s.chatOnce(ctx, router.TopModel, messages, tools, 4000)
		totalTokens += msg.tokens
		if err != nil {
			s.failPlan(a, "Planleggingen feilet: "+err.Error(), totalTokens)
			return
		}

		// Ingen verktøykall: modellen prater i stedet for å jobbe. Dytt den videre.
		if len(msg.calls) == 0 {
			messages = append(messages,
				map[string]any{"role": "assistant", "content": msg.visible},
				map[string]any{"role": "user", "content": "Fortsett utforskningen, eller kall save_plan hvis du er ferdig."},
			)
			continue
		}

		if msg.visible != "" {
			s.saveActivity(agentID, msg.visible, steps)
		}

		calls := make([]any, 0, len(msg.calls))
		toolMsgs := make([]any, 0, len(msg.calls))
		for _, c := range msg.calls {
			calls = append(calls, map[string]any{
				"id": c.id, "type": "function",
				"function": map[string]any{"name": c.name, "arguments": c.args},
			})

			// save_plan: valider stegene ved å faktisk kjøre dem.
			if c.name == "save_plan" {
				plan, problems := s.validatePlan(ctx, dbCtx, c.args)
				if len(problems) == 0 {
					raw, _ := json.Marshal(plan)
					if err := s.store.SetAgentPlan(agentID, string(raw)); err != nil {
						s.failPlan(a, "Kunne ikke lagre planen: "+err.Error(), totalTokens)
						return
					}
					s.store.SetMissionActivity(agentID, "")
					s.store.RecordRun(agentID, store.AgentRun{
						Status: "plan", Output: plan.Approach, TokensUsed: totalTokens,
					})
					s.postToAgentChat(a, planSummary(plan))
					s.log.Info("plan bygget", "agent", a.Name, "steg", len(plan.Steps), "tokens", totalTokens)
					return
				}
				toolMsgs = append(toolMsgs, map[string]any{
					"role": "tool", "tool_call_id": c.id,
					"content": "Planen ble IKKE lagret. Rett disse og kall save_plan på nytt:\n" +
						strings.Join(problems, "\n"),
				})
				continue
			}

			var args struct {
				Query        string `json:"query"`
				URL          string `json:"url"`
				ConnectionID string `json:"connection_id"`
				SQL          string `json:"sql"`
			}
			_ = json.Unmarshal([]byte(c.args), &args)
			var result string
			switch c.name {
			case "query_database":
				result = s.runDBQuery(ctx, dbCtx, args.ConnectionID, args.SQL)
			case "fetch_url":
				result = s.runFetchURL(ctx, args.URL)
			default:
				result, _ = s.runWebSearch(ctx, args.Query)
			}
			if lbl := toolStepLabel(c.name, c.args); lbl != "" {
				steps = append(steps, lbl)
				if len(steps) > 12 {
					steps = steps[len(steps)-12:]
				}
			}
			toolMsgs = append(toolMsgs, map[string]any{
				"role": "tool", "tool_call_id": c.id, "content": truncate(result, planStepChars),
			})
		}
		messages = append(messages, map[string]any{
			"role": "assistant", "content": "", "tool_calls": calls,
		})
		messages = append(messages, toolMsgs...)
	}
	s.failPlan(a, "Fant ingen fremgangsmåte som holdt mål innen rundetaket.", totalTokens)
}

// failPlan markerer planen som ødelagt og forteller brukeren hvorfor. Agenten
// faller da tilbake til fri kjøring, så den fortsatt gjør nytte.
func (s *Server) failPlan(a store.Agent, reason string, tokens int) {
	s.store.SetPlanStatus(a.ID, "broken", reason)
	s.store.SetMissionActivity(a.ID, "")
	s.store.RecordRun(a.ID, store.AgentRun{Status: "plan_error", TokensUsed: tokens, Error: reason})
	s.log.Warn("spinup feilet", "agent", a.Name, "grunn", reason)
	s.postToAgentChat(a, "Jeg fikk ikke satt opp en fast fremgangsmåte: "+reason+
		"\n\nJeg kjører oppgaven fritt inntil videre.")
}

// validatePlan parser og prøvekjører en foreslått plan. Returnerer planen og
// en liste problemer — tom liste betyr at planen er god.
func (s *Server) validatePlan(ctx context.Context, dbCtx *dbToolCtx, rawArgs string) (agentPlan, []string) {
	var plan agentPlan
	if err := json.Unmarshal([]byte(rawArgs), &plan); err != nil {
		return plan, []string{"Ugyldig JSON i save_plan: " + err.Error()}
	}
	var problems []string
	if len(plan.Steps) == 0 {
		problems = append(problems, "Planen har ingen steg.")
	}
	if len(plan.Steps) > planMaxSteps {
		problems = append(problems, fmt.Sprintf("For mange steg (%d) — maks %d. Slå sammen eller kutt.",
			len(plan.Steps), planMaxSteps))
	}
	if strings.TrimSpace(plan.Watch) == "" {
		problems = append(problems, "watch mangler — beskriv hva som skal vurderes hver kjøring.")
	}
	if strings.TrimSpace(plan.AlertRule) == "" {
		problems = append(problems, "alert_rule mangler — beskriv når brukeren skal varsles.")
	}

	for i, st := range plan.Steps {
		n := i + 1
		switch strings.ToLower(strings.TrimSpace(st.Kind)) {
		case "sql":
			if strings.TrimSpace(st.SQL) == "" {
				problems = append(problems, fmt.Sprintf("Steg %d (sql) mangler sql.", n))
				continue
			}
			if dbCtx == nil {
				problems = append(problems, fmt.Sprintf("Steg %d (sql): ingen database er tilgjengelig.", n))
				continue
			}
			res := s.runDBQuery(ctx, dbCtx, st.ConnectionID, st.SQL)
			if planQueryFailed(res) {
				problems = append(problems, fmt.Sprintf("Steg %d (%s) feilet ved prøvekjøring: %s",
					n, st.Label, truncate(res, 600)))
			}
		case "fetch":
			if strings.TrimSpace(st.URL) == "" {
				problems = append(problems, fmt.Sprintf("Steg %d (fetch) mangler url.", n))
				continue
			}
			if res := s.runFetchURL(ctx, st.URL); strings.TrimSpace(res) == "" {
				problems = append(problems, fmt.Sprintf("Steg %d (%s): %s ga ikke noe innhold.",
					n, st.Label, st.URL))
			}
		case "web":
			if strings.TrimSpace(st.Query) == "" {
				problems = append(problems, fmt.Sprintf("Steg %d (web) mangler query.", n))
			}
		default:
			problems = append(problems, fmt.Sprintf("Steg %d har ukjent kind %q — bruk sql, web eller fetch.",
				n, st.Kind))
		}
	}
	return plan, problems
}

// planQueryFailed kjenner igjen feilsvarene runDBQuery gir (den returnerer
// tekst, ikke error, siden svaret ellers går rett til modellen).
func planQueryFailed(res string) bool {
	r := strings.TrimSpace(res)
	if strings.HasPrefix(r, "{") {
		return false
	}
	return true
}

// planSummary er meldingen brukeren får i agent-chatten når planen er klar.
func planSummary(p agentPlan) string {
	var b strings.Builder
	b.WriteString("Klar. Slik gjør jeg det hver kjøring:\n\n")
	for i, st := range p.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, st.Label)
	}
	if p.Approach != "" {
		b.WriteString("\n" + p.Approach + "\n")
	}
	b.WriteString("\nJeg sier fra når: " + p.AlertRule)
	return b.String()
}

// planRun er resultatet av én plan-kjøring.
type planRun struct {
	output    string // det brukerrettede svaret (tomt hvis ingenting er endret)
	tokens    int
	alert     bool // oppfyller alert_rule — verdt å vekke brukeren
	unchanged bool // rådataene er identiske med forrige kjøring
}

// reportTool lar tolkningen svare strukturert, så koden — ikke modellen —
// avgjør hva som postes og varsles om.
var reportTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "report",
		"description": "Lever resultatet av kjøringen. Kall dette én gang, som eneste svar.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "kort svar til brukeren på norsk, med de faktiske tallene",
				},
				"changed": map[string]any{
					"type":        "boolean",
					"description": "true hvis noe reelt har endret seg siden forrige kjøring",
				},
				"alert": map[string]any{
					"type":        "boolean",
					"description": "true KUN hvis regelen for når brukeren skal varsles er oppfylt",
				},
				"stats": map[string]any{
					"type": "array",
					"description": "de viktigste nøkkeltallene, maks 4. Ta med delta når du har " +
						"forrige kjørings tall å sammenligne med.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label": map[string]any{"type": "string", "description": "hva tallet er"},
							"value": map[string]any{"type": "string", "description": "selve tallet"},
							"unit":  map[string]any{"type": "string", "description": "enhet, f.eks. kr eller %"},
							"delta": map[string]any{"type": "string", "description": "endring siden forrige kjøring, f.eks. «+12 %» eller «-4»"},
						},
						"required": []string{"label", "value"},
					},
				},
				"table_step": map[string]any{
					"type": "integer",
					"description": "steg-nummeret hvis radene derfra bør vises som tabell (1 = første steg). " +
						"0 eller utelatt = ingen tabell. Gjengi ALDRI rader i teksten din — oppgi steget her.",
				},
			},
			"required": []string{"summary", "alert"},
		},
	},
}

// stepResult er ett utført steg med rådataene sine, så tabeller kan rendres
// deterministisk fra kilden i stedet for å skrives av modellen.
type stepResult struct {
	label string
	kind  string
	raw   string
}

// planData er alt én plan-kjøring produserte.
type planData struct {
	steps    []stepResult
	text     string // formatert for modellen
	snapshot string // til sammenligning neste kjøring
	failed   int
}

// runPlanSteps utfører stegene deterministisk.
func (s *Server) runPlanSteps(ctx context.Context, a store.Agent, plan agentPlan) planData {
	dbCtx := s.buildDBTool(a.TenantID, a.UserID, "")
	var out planData
	var b strings.Builder
	snap := make([]map[string]string, 0, len(plan.Steps))

	for i, st := range plan.Steps {
		var res string
		kind := strings.ToLower(strings.TrimSpace(st.Kind))
		switch kind {
		case "sql":
			res = s.runDBQuery(ctx, dbCtx, st.ConnectionID, st.SQL)
			if planQueryFailed(res) {
				out.failed++
			}
		case "fetch":
			res = s.runFetchURL(ctx, st.URL)
			if strings.TrimSpace(res) == "" {
				out.failed++
				res = "(ingen innhold)"
			}
		case "web":
			res, _ = s.runWebSearch(ctx, st.Query)
		default:
			continue
		}
		res = truncate(res, planStepChars)
		fmt.Fprintf(&b, "### Steg %d: %s\n%s\n\n", i+1, st.Label, res)
		snap = append(snap, map[string]string{"label": st.Label, "data": res})
		out.steps = append(out.steps, stepResult{label: st.Label, kind: kind, raw: res})
	}
	raw, _ := json.Marshal(snap)
	out.text = b.String()
	out.snapshot = string(raw)
	return out
}

// statBlock bygger ```stat-kortet frontend rendrer. delta er endringen siden
// forrige kjøring, f.eks. «+12 %».
func statBlock(label, value, unit, delta string) string {
	spec := map[string]any{"label": label, "value": value}
	if unit != "" {
		spec["unit"] = unit
	}
	if delta != "" {
		spec["delta"] = delta
	}
	raw, _ := json.Marshal(spec)
	return "```stat\n" + string(raw) + "\n```\n\n"
}

// tableFromStep rendrer et sql-stegs rådata som tabell. Dataene kommer fra
// spørringen, aldri fra modellen, så tabellen kan ikke bli feilskrevet.
func tableFromStep(st stepResult) string {
	if st.kind != "sql" {
		return ""
	}
	var res struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(st.raw), &res); err != nil {
		return ""
	}
	if len(res.Columns) == 0 || len(res.Rows) == 0 {
		return ""
	}
	return tableBlock(res.Columns, res.Rows)
}

// executeAgentPlan kjører en ferdig plan: stegene utføres deterministisk uten
// modellen, og modellen brukes kun til å tolke det som faktisk er nytt. Er
// dataene identiske med forrige kjøring, brukes ingen tokens i det hele tatt.
func (s *Server) executeAgentPlan(ctx context.Context, a store.Agent) (planRun, error) {
	var run planRun
	var plan agentPlan
	if err := json.Unmarshal([]byte(a.Plan), &plan); err != nil {
		return run, fmt.Errorf("ugyldig lagret plan: %w", err)
	}
	if len(plan.Steps) == 0 {
		return run, fmt.Errorf("planen har ingen steg")
	}

	ctx, cancel := context.WithTimeout(ctx, agentHTTPTimeoutSeconds*time.Second)
	defer cancel()

	pd := s.runPlanSteps(ctx, a, plan)

	// Hele planen feilet — kilden eller skjemaet har trolig endret seg.
	// Marker planen som ødelagt og bygg den på nytt.
	if pd.failed == len(plan.Steps) {
		s.store.SetPlanStatus(a.ID, "broken", "alle steg feilet ved kjøring")
		s.startPlanBuild(a.ID)
		return run, fmt.Errorf("planen virker ikke lenger — bygger den på nytt")
	}

	// Identiske rådata: ingenting har skjedd. Ingen tolkning, ingen melding,
	// ingen tokens — agenten tier heller enn å gjenta seg selv.
	if a.LastSnapshot != "" && pd.snapshot == a.LastSnapshot {
		run.unchanged = true
		return run, nil
	}

	system := "Du tolker resultatet av en agent-kjøring for brukeren. Dataene er allerede hentet — du " +
		"skal ikke hente noe mer, og aldri be om tilgang. Har du forrige kjørings data, er jobben din å " +
		"si hva som er ENDRET: nevn tallene før og nå, ikke gjenta det som står stille. Kort og konkret " +
		"på norsk, ingen innledning, ingen spørsmål tilbake. Avslutt med report: legg de viktigste " +
		"tallene i stats (med delta når du har forrige kjørings tall), og pek på et steg med table_step " +
		"hvis radene derfra bør vises — tabellen rendres fra kilden, så du skal aldri skrive av rader."
	userMsg := "Oppgave: " + a.Task +
		"\n\nDette skal vurderes: " + plan.Watch +
		"\nBrukeren skal varsles når: " + plan.AlertRule
	if a.LastSnapshot != "" {
		userMsg += "\n\nForrige kjørings data:\n" + truncate(snapshotText(a.LastSnapshot), planReportChar)
	} else {
		userMsg += "\n\n(Første kjøring — ingen tidligere data å sammenligne med.)"
	}
	userMsg += "\n\nFerske data:\n" + truncate(pd.text, planReportChar)

	msg, err := s.chatOnce(ctx, router.MidModel, []any{
		map[string]any{"role": "system", "content": system},
		map[string]any{"role": "user", "content": userMsg},
	}, []any{reportTool}, 800)
	run.tokens = msg.tokens
	if err != nil {
		return run, err
	}

	run.output = msg.visible
	for _, c := range msg.calls {
		if c.name != "report" {
			continue
		}
		var rep struct {
			Summary   string       `json:"summary"`
			Changed   bool         `json:"changed"`
			Alert     bool         `json:"alert"`
			Stats     []reportStat `json:"stats"`
			TableStep int          `json:"table_step"`
		}
		if err := json.Unmarshal([]byte(c.args), &rep); err == nil {
			run.alert = rep.Alert
			run.output = composeReport(rep.Summary, rep.Stats, rep.TableStep, pd.steps)
		}
		break
	}

	s.store.SetAgentSnapshot(a.ID, pd.snapshot)
	return run, nil
}

// reportStat er ett nøkkeltall modellen løftet frem.
type reportStat struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
	Delta string `json:"delta"`
}

// composeReport setter sammen meldingen brukeren ser: nøkkeltall øverst, så
// teksten, så en tabell rendret fra rådataene. Maks fire kort, så kjøringen
// leses på ett blikk.
func composeReport(summary string, stats []reportStat, tableStep int, steps []stepResult) string {
	var b strings.Builder
	for i, s := range stats {
		if i == 4 {
			break
		}
		if strings.TrimSpace(s.Label) == "" || strings.TrimSpace(s.Value) == "" {
			continue
		}
		b.WriteString(statBlock(s.Label, s.Value, s.Unit, s.Delta))
	}
	b.WriteString(strings.TrimSpace(summary))
	if tableStep >= 1 && tableStep <= len(steps) {
		if tbl := tableFromStep(steps[tableStep-1]); tbl != "" {
			b.WriteString("\n\n" + tbl)
		}
	}
	return strings.TrimSpace(b.String())
}

// snapshotText gjør et lagret snapshot lesbart igjen for sammenligning.
func snapshotText(snapshot string) string {
	var snap []map[string]string
	if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
		return snapshot
	}
	var b strings.Builder
	for i, st := range snap {
		fmt.Fprintf(&b, "### Steg %d: %s\n%s\n\n", i+1, st["label"], st["data"])
	}
	return b.String()
}

// truncate kutter tekst til n tegn og markerer at det er kuttet.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (kuttet)"
}
