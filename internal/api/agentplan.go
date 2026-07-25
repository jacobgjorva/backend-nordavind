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

// executeAgentPlan kjører en ferdig plan: stegene utføres deterministisk uten
// modellen, og modellen brukes kun til å tolke resultatet. Det gjør hver
// kjøring billig og tallene konsistente over tid.
func (s *Server) executeAgentPlan(ctx context.Context, a store.Agent) (string, int, error) {
	var plan agentPlan
	if err := json.Unmarshal([]byte(a.Plan), &plan); err != nil {
		return "", 0, fmt.Errorf("ugyldig lagret plan: %w", err)
	}
	if len(plan.Steps) == 0 {
		return "", 0, fmt.Errorf("planen har ingen steg")
	}

	ctx, cancel := context.WithTimeout(ctx, agentHTTPTimeoutSeconds*time.Second)
	defer cancel()

	dbCtx := s.buildDBTool(a.TenantID, a.UserID, "")

	var data strings.Builder
	var failed int
	for i, st := range plan.Steps {
		var res string
		switch strings.ToLower(strings.TrimSpace(st.Kind)) {
		case "sql":
			res = s.runDBQuery(ctx, dbCtx, st.ConnectionID, st.SQL)
			if planQueryFailed(res) {
				failed++
			}
		case "fetch":
			res = s.runFetchURL(ctx, st.URL)
			if strings.TrimSpace(res) == "" {
				failed++
				res = "(ingen innhold)"
			}
		case "web":
			res, _ = s.runWebSearch(ctx, st.Query)
		default:
			continue
		}
		fmt.Fprintf(&data, "### Steg %d: %s\n%s\n\n", i+1, st.Label, truncate(res, planStepChars))
	}

	// Hele planen feilet — kilden eller skjemaet har trolig endret seg.
	// Marker planen som ødelagt og bygg den på nytt.
	if failed == len(plan.Steps) {
		s.store.SetPlanStatus(a.ID, "broken", "alle steg feilet ved kjøring")
		s.startPlanBuild(a.ID)
		return "", 0, fmt.Errorf("planen virker ikke lenger — bygger den på nytt")
	}

	system := "Du tolker resultatet av en agent-kjøring for brukeren. Dataene under er allerede hentet " +
		"— du skal ikke hente noe mer, og aldri be om tilgang. Svar kort og konkret på norsk, med de " +
		"faktiske tallene. Ingen innledning, ingen spørsmål tilbake. Er det ingenting å melde, si det " +
		"med én setning."
	userMsg := "Oppgave: " + a.Task +
		"\n\nDette skal vurderes: " + plan.Watch +
		"\nVerdt å si fra om: " + plan.AlertRule +
		"\n\nFerske data:\n" + truncate(data.String(), planReportChar)

	msg, err := s.chatOnce(ctx, router.MidModel, []any{
		map[string]any{"role": "system", "content": system},
		map[string]any{"role": "user", "content": userMsg},
	}, nil, 800)
	if err != nil {
		return "", msg.tokens, err
	}
	return msg.visible, msg.tokens, nil
}

// truncate kutter tekst til n tegn og markerer at det er kuttet.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (kuttet)"
}
