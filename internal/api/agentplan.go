package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// agentChart er grafen agenten viser sammen med rapporten. Den lagres som en
// vanlig widget, så den henter ferske tall selv hver gang meldingen vises.
type agentChart struct {
	Type         string `json:"type"` // line | bar | donut
	Title        string `json:"title"`
	ConnectionID string `json:"connection_id"`
	SQL          string `json:"sql"`
	X            string `json:"x"` // kategori-/tidskolonne
	Y            string `json:"y"` // verdikolonne
	Group        string `json:"group,omitempty"`
}

// agentPlan er resultatet av spinup: de konkrete stegene som skal kjøres hver
// gang, hva som skal følges med på, og når det er verdt å si fra.
type agentPlan struct {
	Approach  string      `json:"approach"`   // kort begrunnelse for valgt fremgangsmåte
	Steps     []agentStep `json:"steps"`      // utføres i rekkefølge, deterministisk
	Watch     string      `json:"watch"`      // hva som skal vurderes i resultatet
	AlertRule string      `json:"alert_rule"` // når det er verdt å varsle brukeren

	Chart     *agentChart `json:"chart,omitempty"`      // valgfri graf
	ChartSlug string      `json:"chart_slug,omitempty"` // widgeten grafen bor i

	// Mail: satt når oppgaven eksplisitt ber agenten sende e-post. Mottaker og
	// emne er faste; kroppen skrives av tolkningen hver kjøring, og agenten
	// sender selv — agenter er unntaket fra chat-regelen om manuell sending.
	Mail *agentPlanMail `json:"mail,omitempty"`
}

// agentPlanMail er planens faste mail-oppsett.
type agentPlanMail struct {
	ToName  string `json:"to_name,omitempty"`
	ToEmail string `json:"to_email"`
	Subject string `json:"subject"`
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
				"mail": map[string]any{
					"type": "object",
					"description": "KUN når oppgaven eksplisitt ber agenten sende e-post: fast mottaker " +
						"og emne. Kroppen skrives automatisk per kjøring fra ferske data.",
					"properties": map[string]any{
						"to_name":  map[string]any{"type": "string"},
						"to_email": map[string]any{"type": "string", "description": "mottakerens e-postadresse"},
						"subject":  map[string]any{"type": "string", "description": "fast emnelinje"},
					},
					"required": []string{"to_email", "subject"},
				},
				"chart": map[string]any{
					"type": "object",
					"description": "valgfri graf som vises sammen med hver rapport. Ta den med når " +
						"utviklingen over tid eller fordelingen mellom kategorier sier mer enn et tall. " +
						"Grafen henter ferske data selv, så spørringen må være tidsrelativ.",
					"properties": map[string]any{
						"type":          map[string]any{"type": "string", "description": "line (utvikling over tid), bar (sammenligning) eller donut (fordeling)"},
						"title":         map[string]any{"type": "string", "description": "kort tittel over grafen"},
						"connection_id": map[string]any{"type": "string"},
						"sql":           map[string]any{"type": "string", "description": "SELECT som gir én rad per punkt"},
						"x":             map[string]any{"type": "string", "description": "kolonnen langs x-aksen — MÅ finnes i spørringen"},
						"y":             map[string]any{"type": "string", "description": "verdikolonnen langs y-aksen — MÅ finnes i spørringen"},
						"group":         map[string]any{"type": "string", "description": "valgfri kolonne som summerer y per verdi"},
					},
					"required": []string{"type", "title", "sql", "x", "y"},
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
	"- Vurder om en graf hører hjemme i rapporten. Utvikling over tid eller fordeling mellom kategorier " +
	"forstås raskere som graf enn som tall. Er svaret ett enkelt tall, dropp grafen. Både x og y skal " +
	"være ekte kolonner fra spørringen — grafen skal aldri ha en umerket akse.\n" +
	"- Ber oppgaven eksplisitt om at agenten sender e-post: sett mail-feltet (mottaker + fast emne). " +
	"Kroppen skrives automatisk per kjøring. Ikke lag egne steg for e-post.\n" +
	"AVSLUTT med save_plan: stegene som skal kjøres hver gang, hva som skal vurderes (watch), når " +
	"det er verdt å varsle brukeren (alert_rule, med konkrete terskler der det gir mening), og " +
	"eventuelt chart. " +
	"Server-siden prøvekjører stegene dine — får du dem i retur med feil, rett dem og lagre på nytt."

// handleGetAgentPlan returnerer agentens kompilerte plan til flyt-visningen.
func (s *Server) handleGetAgentPlan(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	a, err := s.store.GetAgent(r.PathValue("id"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	var plan agentPlan
	if strings.TrimSpace(a.Plan) != "" {
		_ = json.Unmarshal([]byte(a.Plan), &plan)
	}
	writeJSON(w, map[string]any{
		"plan":             plan,
		"status":           a.PlanStatus,
		"error":            a.PlanError,
		"schedule_label":   a.ScheduleLabel,
		"task":             a.Task,
		"agent_name":       a.Name,
		"interval_seconds": a.IntervalSeconds,
	})
}

// handleSaveAgentPlan lagrer en brukerredigert plan. Stegene prøvekjøres
// først — nøyaktig samme validering som spinup bruker — så en ødelagt
// spørring aldri kan lagres.
func (s *Server) handleSaveAgentPlan(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	a, err := s.store.GetAgent(id, user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	dbCtx := s.buildDBTool(user.TenantID, user.ID, "")
	plan, problems := s.validatePlan(r.Context(), dbCtx, string(raw))
	if len(problems) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"problems": problems})
		return
	}
	// Grafen kan ha blitt endret — oppdater widgeten den bor i.
	a.TenantID = user.TenantID
	a.UserID = user.ID
	s.ensureChartWidget(a, &plan)
	out, _ := json.Marshal(plan)
	if err := s.store.SetAgentPlan(id, string(out)); err != nil {
		http.Error(w, "kunne ikke lagre", http.StatusInternalServerError)
		return
	}
	s.log.Info("plan redigert av bruker", "agent", id, "steg", len(plan.Steps))
	writeJSON(w, map[string]any{"plan": plan})
}

// handleRebuildAgentPlan kaster planen og kjører spinup på nytt.
func (s *Server) handleRebuildAgentPlan(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.GetAgent(id, user.ID); err != nil {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	s.store.ClearAgentPlan(id)
	s.startPlanBuild(id)
	w.WriteHeader(http.StatusNoContent)
}

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
					s.ensureChartWidget(a, &plan)
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
	problems = append(problems, s.validateChart(ctx, dbCtx, plan.Chart)...)
	if m := plan.Mail; m != nil {
		if !strings.Contains(m.ToEmail, "@") {
			problems = append(problems, "Mail-mottakeren mangler gyldig e-postadresse.")
		}
		if strings.TrimSpace(m.Subject) == "" {
			problems = append(problems, "Mail-oppsettet mangler emne.")
		}
	}
	return plan, problems
}

// chartTypes er grafene widget-motoren rendrer.
var chartTypes = map[string]bool{"line": true, "bar": true, "donut": true}

// validateChart prøvekjører grafens spørring og sjekker at både x- og y-kolonnen
// faktisk finnes i resultatet — ellers ville grafen blitt tom eller uten akse.
func (s *Server) validateChart(ctx context.Context, dbCtx *dbToolCtx, c *agentChart) []string {
	if c == nil {
		return nil
	}
	var problems []string
	if !chartTypes[strings.ToLower(strings.TrimSpace(c.Type))] {
		problems = append(problems, fmt.Sprintf("Grafen har ukjent type %q — bruk line, bar eller donut.", c.Type))
	}
	if strings.TrimSpace(c.SQL) == "" {
		return append(problems, "Grafen mangler sql.")
	}
	if strings.TrimSpace(c.X) == "" || strings.TrimSpace(c.Y) == "" {
		problems = append(problems, "Grafen må ha både x og y — begge aksene skal være merket.")
	}
	if dbCtx == nil {
		return append(problems, "Grafen krever database, men ingen er tilgjengelig.")
	}

	res := s.runDBQuery(ctx, dbCtx, c.ConnectionID, c.SQL)
	if planQueryFailed(res) {
		return append(problems, "Grafens spørring feilet ved prøvekjøring: "+truncate(res, 600))
	}
	var out struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		return append(problems, "Kunne ikke lese grafens data: "+err.Error())
	}
	if len(out.Rows) == 0 {
		problems = append(problems, "Grafens spørring gir ingen rader — grafen ville vært tom.")
	}
	has := func(col string) bool {
		for _, c := range out.Columns {
			if strings.EqualFold(c, col) {
				return true
			}
		}
		return false
	}
	for field, col := range map[string]string{"x": c.X, "y": c.Y} {
		if col != "" && !has(col) {
			problems = append(problems, fmt.Sprintf("Grafens %s-kolonne %q finnes ikke i spørringen. Kolonner: %s",
				field, col, strings.Join(out.Columns, ", ")))
		}
	}
	if g := strings.TrimSpace(c.Group); g != "" && !has(g) {
		problems = append(problems, fmt.Sprintf("Grafens group-kolonne %q finnes ikke i spørringen.", g))
	}
	return problems
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

// ensureChartWidget lagrer grafen som en vanlig widget og setter slug'en på
// planen. Widgeten henter data selv, så grafen er fersk hver gang rapporten
// vises — uten at agenten bruker et eneste token på den.
func (s *Server) ensureChartWidget(a store.Agent, plan *agentPlan) {
	c := plan.Chart
	if c == nil {
		return
	}
	slug := chartSlug(a)
	spec := map[string]any{
		"type":          strings.ToLower(strings.TrimSpace(c.Type)),
		"title":         c.Title,
		"connection_id": c.ConnectionID,
		"sql":           c.SQL,
		"x":             c.X,
		"y":             c.Y,
	}
	if g := strings.TrimSpace(c.Group); g != "" {
		spec["group"] = g
	}
	raw, _ := json.Marshal(spec)

	// Finnes den fra en tidligere plan, skal den bare oppdateres.
	if _, err := s.store.Widget(slug, a.UserID); err != nil {
		if _, err := s.store.CreateWidget(a.TenantID, a.UserID, slug, c.Title); err != nil {
			s.log.Warn("kunne ikke opprette agent-graf", "agent", a.ID, "slug", slug, "err", err)
			plan.Chart = nil
			return
		}
	}
	if err := s.store.SetWidget(slug, a.UserID, c.Title, string(raw)); err != nil {
		s.log.Warn("kunne ikke lagre agent-graf", "agent", a.ID, "slug", slug, "err", err)
		plan.Chart = nil
		return
	}
	plan.ChartSlug = slug
	s.log.Info("agent-graf lagret", "agent", a.Name, "slug", slug, "type", spec["type"])
}

// deleteAgentChart fjerner grafen som hørte til agenten, så en slettet agent
// ikke etterlater en widget i brukerens slash-meny.
func (s *Server) deleteAgentChart(agentID, userID string) {
	a, err := s.store.GetAgent(agentID, userID)
	if err != nil || strings.TrimSpace(a.Plan) == "" {
		return
	}
	var plan agentPlan
	if err := json.Unmarshal([]byte(a.Plan), &plan); err != nil || plan.ChartSlug == "" {
		return
	}
	if err := s.store.DeleteWidget(plan.ChartSlug, userID); err != nil {
		s.log.Warn("kunne ikke slette agent-graf", "slug", plan.ChartSlug, "err", err)
	}
}

// chartSlug gir agenten en stabil, unik slug, så en ny plan overtar den samme
// grafen i stedet for å legge igjen foreldreløse widgets.
func chartSlug(a store.Agent) string {
	id := a.ID
	if len(id) > 8 {
		id = id[:8]
	}
	base := slugify(a.Name)
	if base == "" {
		base = "agent"
	}
	return slugify(base + "-" + id)
}

// planSummary er meldingen brukeren får i agent-chatten når planen er klar.
func planSummary(p agentPlan) string {
	var b strings.Builder
	b.WriteString("Klar. Slik gjør jeg det hver kjøring:\n\n")
	for i, st := range p.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, st.Label)
	}
	if p.Chart != nil && p.ChartSlug != "" {
		fmt.Fprintf(&b, "\nJeg viser også grafen «%s».\n", p.Chart.Title)
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
				"mail_body": map[string]any{
					"type": "string",
					"description": "KUN når planen har mail-oppsett: e-postkroppen for denne kjøringen, " +
						"norsk, uten signatur, med de faktiske tallene",
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
	// ingen tokens — agenten tier heller enn å gjenta seg selv. UNNTAK: en
	// mail-plan skal levere e-post hver kjøring uansett.
	if plan.Mail == nil && a.LastSnapshot != "" && pd.snapshot == a.LastSnapshot {
		run.unchanged = true
		return run, nil
	}

	system := "Du tolker resultatet av en agent-kjøring for brukeren. Dataene er allerede hentet — du " +
		"skal ikke hente noe mer, og aldri be om tilgang. Har du forrige kjørings data, er jobben din å " +
		"si hva som er ENDRET: nevn tallene før og nå, ikke gjenta det som står stille. Kort og konkret " +
		"på norsk, ingen innledning, ingen spørsmål tilbake. Avslutt med report: legg de viktigste " +
		"tallene i stats (med delta når du har forrige kjørings tall), og pek på et steg med table_step " +
		"hvis radene derfra bør vises — tabellen rendres fra kilden, så du skal aldri skrive av rader."
	if plan.Mail != nil {
		system += " Planen har mail-oppsett: fyll ALLTID mail_body i report med e-postkroppen for " +
			"denne kjøringen — den sendes automatisk til den faste mottakeren."
	}
	if p := strings.TrimSpace(a.Personality); p != "" {
		system += " Du har personligheten «" + p + "» — la den farge ordvalget i summary, men aldri " +
			"tallene, presisjonen eller varselvurderingen."
	}
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
			MailBody  string       `json:"mail_body"`
		}
		if err := json.Unmarshal([]byte(c.args), &rep); err == nil {
			run.alert = rep.Alert
			run.output = composeReport(rep.Summary, rep.Stats, rep.TableStep, pd.steps, plan.ChartSlug)
			// Mail-plan: send e-posten med kroppen tolkningen skrev.
			if plan.Mail != nil && strings.TrimSpace(rep.MailBody) != "" {
				if err := s.sendAgentMail(a, plan.Mail.ToName, plan.Mail.ToEmail, plan.Mail.Subject, rep.MailBody); err != nil {
					s.log.Warn("agent-mail feilet", "id", a.ID, "err", err)
					run.output += "\n\n(Klarte ikke sende e-posten: " + err.Error() + ")"
				} else {
					run.output += "\n\n_E-post sendt til " + plan.Mail.ToEmail + "._"
				}
			}
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
func composeReport(summary string, stats []reportStat, tableStep int, steps []stepResult, chartSlug string) string {
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
	if chartSlug != "" {
		b.WriteString("\n\n```widget\n" + chartSlug + "\n```\n")
	}
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
