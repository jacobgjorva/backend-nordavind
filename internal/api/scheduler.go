package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/mail"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

const (
	schedulerTick           = 30 * time.Second
	agentMaxRounds          = 4
	missionMaxRounds        = 20
	agentHTTPTimeoutSeconds = 120
)

// startScheduler kjører en løkke som utfører forfalne agenter. Den poller
// hyppig, men gjør lite når ingenting er forfalt.
func (s *Server) startScheduler(ctx context.Context) {
	ticker := time.NewTicker(schedulerTick)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runDueAgents(ctx)
			}
		}
	}()
	// Gjenoppta kontinuerlige oppdrag som var i gang (også etter omstart).
	s.resumeMissions(ctx)

	// Live OneDrive-eksporter: push ferske tall inn i arbeidsbøkene fast.
	pushTicker := time.NewTicker(15 * time.Minute)
	go func() {
		defer pushTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pushTicker.C:
				s.pushOneDriveExports(ctx)
			}
		}
	}()
}

// pushOneDriveExports kjører hver lagrede spørring på nytt og erstatter
// innholdet i OneDrive-arbeidsboka, så fila alltid har ferske tall.
func (s *Server) pushOneDriveExports(ctx context.Context) {
	exports, err := s.store.ListAllOneDriveExports()
	if err != nil {
		s.log.Error("kunne ikke hente onedrive-eksporter", "err", err)
		return
	}
	for _, e := range exports {
		token, err := s.msAccessToken(ctx, e.UserID)
		if err != nil {
			continue // frakoblet konto — hopp stille over
		}
		cols, rows, err := s.runStoredQuery(ctx, e.TenantID, e.UserID, e.ConnectionID, e.SQL)
		if err != nil {
			s.log.Warn("onedrive-push: spørring feilet", "id", e.ID, "err", err)
			continue
		}
		if len(rows) > maxExportRows {
			rows = rows[:maxExportRows]
		}
		data, err := xlsxBytes(e.Title, cols, rows)
		if err != nil {
			continue
		}
		if _, _, err := s.graphUploadXLSX(ctx, token, safeFilename(e.Title), data); err != nil {
			s.log.Warn("onedrive-push feilet", "id", e.ID, "err", err)
			continue
		}
		s.store.TouchOneDriveExport(e.ID)
	}
}

// resumeMissions starter løkker for alle godkjente, aktive oppdrag.
func (s *Server) resumeMissions(ctx context.Context) {
	agents, err := s.store.RunningMissions()
	if err != nil {
		s.log.Error("kunne ikke hente aktive oppdrag", "err", err)
		return
	}
	for _, a := range agents {
		s.startMissionRunner(ctx, a.ID)
	}
}

// missionLoopPause er en kort pust mellom rundene, så en pause/stopp rekker å
// slå inn og vi ikke hamrer upstream helt uten mellomrom.
const missionLoopPause = 2 * time.Second

// startMissionRunner kjører oppdraget som en kontinuerlig løkke: runde etter
// runde til målet (kriteriene) er nådd, token-taket er truffet, eller brukeren
// pauser. Idempotent — samme oppdrag startes aldri to ganger samtidig.
func (s *Server) startMissionRunner(ctx context.Context, agentID string) {
	s.missionMu.Lock()
	if s.missionRunning[agentID] {
		s.missionMu.Unlock()
		return
	}
	s.missionRunning[agentID] = true
	s.missionMu.Unlock()

	// Vis aktivitet med en gang, så det starter synlig i det brukeren trykker Start.
	s.saveActivity(agentID, "Setter i gang …", nil)

	go func() {
		defer func() {
			s.missionMu.Lock()
			delete(s.missionRunning, agentID)
			s.missionMu.Unlock()
		}()
		for {
			if ctx.Err() != nil {
				return
			}
			a, err := s.store.MissionAgent(agentID)
			if err != nil {
				return
			}
			// Stopp hvis pauset/ferdig eller ikke lenger et godkjent oppdrag.
			if !a.Enabled || a.MissionStatus != "running" || !a.CriteriaApproved {
				return
			}
			// Token-tak: pause og gi beskjed i stedet for å brenne i det uendelige.
			if a.MissionBudget > 0 {
				spent, _ := s.store.MissionTokensSpent(a.ID)
				if spent >= a.MissionBudget {
					s.store.PauseMission(a.ID, "paused")
					s.postToAgentChat(a, "Token-taket er nådd før målet var i mål. Jeg har pauset. Øk taket for å la meg fortsette.")
					return
				}
			}

			output, tokens, done, notify, err := s.executeMission(ctx, a)
			if err != nil {
				s.store.RecordRun(a.ID, store.AgentRun{Status: "error", TokensUsed: tokens, Error: err.Error()})
				// Ikke drep løkka på en enkelt feil — pust og prøv igjen.
				select {
				case <-ctx.Done():
					return
				case <-time.After(missionLoopPause):
				}
				continue
			}
			s.store.RecordRun(a.ID, store.AgentRun{Status: "ok", Output: output, TokensUsed: tokens})
			// Poster KUN ved en konklusjon (mål nådd, eller agenten flagget notify).
			// Ellers jobber den videre; aktiviteten vises live via mission_activity.
			if (done || notify) && strings.TrimSpace(output) != "" {
				s.postToAgentChat(a, output)
			}
			if done {
				s.store.SetMissionActivity(a.ID, "")
				s.store.FinishMission(a.ID)
				if a.PushEnabled {
					go s.maybePush(a, output)
				}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(missionLoopPause):
			}
		}
	}()
}

// runDueAgents kjører alle agenter som er forfalt akkurat nå.
func (s *Server) runDueAgents(ctx context.Context) {
	now := time.Now()
	due, err := s.store.DueAgents(now)
	if err != nil {
		s.log.Error("kunne ikke hente forfalte agenter", "err", err)
		return
	}
	for _, a := range due {
		s.runAgentOnce(ctx, a, now)
	}
}

// runAgentOnce utfører én agent, poster resultatet i agentens chat, logger
// kjøringen og planlegger neste.
func (s *Server) runAgentOnce(ctx context.Context, a store.Agent, now time.Time) {
	// Reschedule først, så en feilende agent ikke kjøres i loop.
	if err := s.store.RescheduleAgent(a, now); err != nil {
		s.log.Error("kunne ikke omplanlegge agent", "id", a.ID, "err", err)
	}

	// Håndhev daglig token-grense.
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if a.DailyTokenLimit > 0 {
		used, _ := s.store.AgentTokensToday(a.ID, midnight)
		if used >= a.DailyTokenLimit {
			s.store.RecordRun(a.ID, store.AgentRun{Status: "skipped", Error: "daglig token-grense nådd"})
			return
		}
	}

	// Oppdrag håndteres av den kontinuerlige løkka (startMissionRunner), ikke her.
	output, tokens, err := s.executeAgent(ctx, a)
	if err != nil {
		s.log.Warn("agent-kjøring feilet", "id", a.ID, "err", err)
		s.store.RecordRun(a.ID, store.AgentRun{Status: "error", TokensUsed: tokens, Error: err.Error()})
		s.postToAgentChat(a, "Kjøringen feilet: "+err.Error())
		return
	}
	s.store.RecordRun(a.ID, store.AgentRun{Status: "ok", Output: output, TokensUsed: tokens})
	s.postToAgentChat(a, output)
	s.log.Info("agent kjørt", "id", a.ID, "navn", a.Name, "tokens", tokens)

	// Push-varsel: kun hvis brukeren har slått det på OG resultatet er verdt å
	// varsle om (billig sjekk), så en rutinekjøring uten nytt ikke maser.
	if a.PushEnabled {
		go s.maybePush(a, output)
	}
}

const pushClassifySystem = "Du vurderer et resultat fra en automatisk agent-kjøring. Er dette verdt å " +
	"vekke brukeren med et push-varsel — altså noe NYTT, handlingskrevende eller viktig? Rutine, " +
	"«ingenting nytt», tomme eller trivielle resultater skal IKKE varsles. Svar KUN «ja» eller «nei»."

// maybePush avgjør billig om agentens output er verdt et varsel, og køer det.
func (s *Server) maybePush(a store.Agent, output string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := strings.TrimSpace(output)
	if body == "" {
		return
	}
	head := body
	if len(head) > 1200 {
		head = head[:1200]
	}
	raw, err := s.llmComplete(ctx, pushClassifySystem, head, 3)
	if err != nil || !strings.Contains(strings.ToLower(raw), "ja") {
		return
	}
	title := a.Name
	if len(body) > 200 {
		body = body[:200]
	}
	if err := s.store.EnqueuePush(a.TenantID, a.UserID, a.ID, title, body); err != nil {
		s.log.Warn("kunne ikke køe push", "id", a.ID, "err", err)
		return
	}
	s.log.Info("push køet", "agent", a.Name, "user", a.UserID)
}

// postToAgentChat legger agentens resultat inn som en melding i chatten.
func (s *Server) postToAgentChat(a store.Agent, content string) {
	if a.ChatID == "" || strings.TrimSpace(content) == "" {
		return
	}
	stamp := time.Now().Format("02.01 15:04")
	body := fmt.Sprintf("**%s**\n\n%s", stamp, content)
	if err := s.store.AppendAgentMessage(a.ChatID, store.ChatMessage{
		Role: "assistant", Content: body,
	}); err != nil {
		s.log.Error("kunne ikke poste agent-melding", "id", a.ID, "err", err)
	}
}

// executeAgent kjører agentens oppgave headless via modellen med verktøy, og
// returnerer sluttsvaret og token-forbruket.
func (s *Server) executeAgent(ctx context.Context, a store.Agent) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, agentHTTPTimeoutSeconds*time.Second)
	defer cancel()

	tools := []any{webSearchTool}
	// Gi agenten samme DB-kontekst som interaktiv chat: ALLE eierens
	// koblinger, ikke bare den bundne. resolveConn auto-ruter til riktig
	// database ut fra tabellene spørringen nevner, så agenten finner alltid
	// dataene selv om a.ConnectionID mangler eller peker feil.
	dbCtx := s.buildDBTool(a.TenantID, a.UserID, "")
	if dbCtx != nil {
		tools = append(tools, dbCtx.tool)
	}

	system := "Du er en autonom agent. Systemet håndterer tidsplan og frekvens — du skal utføre " +
		"oppgaven ÉN gang nå. Ignorer ord som «hvert minutt/daglig» i oppgaven; det styrer bare " +
		"hvor ofte du kjøres. Si ALDRI at du ikke kan kjøre automatisk, planlagt eller kontinuerlig — " +
		"bare lever resultatet direkte. Kort, konkret svar på norsk, ingen innledning eller spørsmål tilbake. " +
		"Kun lesing — databasen er skrivebeskyttet, du kan aldri endre data."
	if dbCtx != nil {
		system += " Du har tilgang til bedriftens database via verktøyet query_database — " +
			"bruk det når oppgaven krever data derfra, aldri si at du mangler tilgang."
		if _, ok := dbCtx.conns[a.ConnectionID]; ok {
			system += " Bruk connection_id=" + a.ConnectionID + " med mindre oppgaven tydelig gjelder en annen database."
		}
	}

	messages := []any{
		map[string]any{"role": "system", "content": system},
		map[string]any{"role": "user", "content": a.Task},
	}

	var totalTokens int
	for round := 0; round <= agentMaxRounds; round++ {
		payload := map[string]any{
			"model":       router.MidModel,
			"messages":    messages,
			"stream":      false,
			"tools":       tools,
			"temperature": 0.3,
			"max_tokens":  800,
			"reasoning":   map[string]any{"enabled": false},
		}
		if round == agentMaxRounds {
			payload["tool_choice"] = "none" // tving et sluttsvar
		}
		body, _ := json.Marshal(payload)
		req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
		if err != nil {
			return "", totalTokens, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return "", totalTokens, err
		}
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
		dec := json.NewDecoder(resp.Body)
		derr := dec.Decode(&out)
		resp.Body.Close()
		if derr != nil {
			return "", totalTokens, derr
		}
		totalTokens += out.Usage.PromptTokens + out.Usage.CompletionTokens
		if len(out.Choices) == 0 {
			return "", totalTokens, fmt.Errorf("tomt svar fra modellen")
		}
		msg := out.Choices[0].Message

		// Normaliser verktøykall: strukturerte + evt. tekstlige
		// <tool_call>-blokker modellen la i innholdet (samme robusthet som
		// streaming-relayet).
		type normCall struct{ id, name, args string }
		var norm []normCall
		for _, c := range msg.ToolCalls {
			norm = append(norm, normCall{c.ID, c.Function.Name, c.Function.Arguments})
		}
		visible, _, blocks := splitToolCallText(msg.Content)
		for i, b := range blocks {
			if name, args := parseTextualToolCall(b); name != "" {
				norm = append(norm, normCall{fmt.Sprintf("call_t%d", i), name, args})
			}
		}

		if len(norm) == 0 {
			return strings.TrimSpace(visible), totalTokens, nil
		}

		// Stopp hvis token-grensen er passert underveis.
		if a.DailyTokenLimit > 0 && totalTokens >= a.DailyTokenLimit {
			return strings.TrimSpace(visible), totalTokens, nil
		}

		// Registrer assistant-meldingen med verktøykallene og utfør dem.
		calls := make([]any, 0, len(norm))
		toolMsgs := make([]any, 0, len(norm))
		for _, c := range norm {
			calls = append(calls, map[string]any{
				"id": c.id, "type": "function",
				"function": map[string]any{"name": c.name, "arguments": c.args},
			})
			var args struct {
				Query        string `json:"query"`
				ConnectionID string `json:"connection_id"`
				SQL          string `json:"sql"`
			}
			_ = json.Unmarshal([]byte(c.args), &args)
			var result string
			if c.name == "query_database" {
				result = s.runDBQuery(ctx, dbCtx, args.ConnectionID, args.SQL)
			} else {
				result, _ = s.runWebSearch(ctx, args.Query)
			}
			toolMsgs = append(toolMsgs, map[string]any{
				"role": "tool", "tool_call_id": c.id, "content": result,
			})
		}
		messages = append(messages, map[string]any{
			"role": "assistant", "content": "", "tool_calls": calls,
		})
		messages = append(messages, toolMsgs...)
	}
	return "", totalTokens, fmt.Errorf("nådde maks antall verktøyrunder uten svar")
}

// missionTools bygger verktøysettet for en oppdrags-agent. send_mail tas kun med
// når agenten har fått tillatelse; ellers foreslår den mail via mission_update.
func missionTools(dbCtx *dbToolCtx, canSend bool) []any {
	tools := []any{webSearchTool, fetchURLTool}
	if dbCtx != nil {
		tools = append(tools, dbCtx.tool)
	}
	if canSend {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "send_mail",
				"description": "Send en e-post på brukerens vegne (du har fått tillatelse). Bruk kun når " +
					"det trengs for å nå målet, og til én konkret mottaker.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"to_name":  map[string]any{"type": "string", "description": "mottakerens navn"},
						"to_email": map[string]any{"type": "string", "description": "mottakerens e-postadresse"},
						"subject":  map[string]any{"type": "string"},
						"body":     map[string]any{"type": "string", "description": "meldingen, norsk, uten signatur"},
					},
					"required": []string{"to_email", "subject", "body"},
				},
			},
		})
	}
	tools = append(tools, map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "mission_update",
			"description": "Kall dette ÉN gang på slutten av kjøringen for å oppdatere oppdraget: hva du " +
				"har funnet, hva som gjenstår, og om målet er nådd. Dette avslutter kjøringen.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary":    map[string]any{"type": "string", "description": "detaljert funn-register: faktum + tall + kilde per punkt (ikke et vagt sammendrag), som neste runde bygger videre på"},
					"next_steps": map[string]any{"type": "string", "description": "hva som gjenstår neste kjøring (tom hvis ferdig)"},
					"status":     map[string]any{"type": "string", "description": "continue = mer arbeid gjenstår, done = målet er nådd"},
					"result":     map[string]any{"type": "string", "description": "det brukerrettede resultatet — vises i chatten KUN når du konkluderer (done) eller notify=true"},
					"notify":      map[string]any{"type": "boolean", "description": "true KUN når du har en konklusjon verdt å poste i chatten nå. Ellers false — da jobber du bare videre, aktiviteten din vises live uansett"},
					"mail": map[string]any{
						"type":        "object",
						"description": "KUN hvis du IKKE har send_mail-tillatelse og vil foreslå en mail brukeren selv sender",
						"properties": map[string]any{
							"name":    map[string]any{"type": "string"},
							"email":   map[string]any{"type": "string"},
							"subject": map[string]any{"type": "string"},
							"body":    map[string]any{"type": "string"},
						},
					},
				},
				"required": []string{"summary", "status"},
			},
		},
	})
	return tools
}

// missionUpdate er feltene mission_update-verktøyet gir.
type missionUpdate struct {
	Summary   string `json:"summary"`
	NextSteps string `json:"next_steps"`
	Status    string `json:"status"`
	Result    string `json:"result"`
	Notify    bool   `json:"notify"`
	Mail      *struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	} `json:"mail"`
}

// executeMission kjører én runde av et oppdrag: laster den komprimerte tilstanden,
// jobber mot målet med verktøy, og lagrer ny tilstand. Returnerer det brukerrettede
// resultatet, token-forbruk, om målet er nådd, og om resultatet skal postes i chatten.
func (s *Server) executeMission(ctx context.Context, a store.Agent) (string, int, bool, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, agentHTTPTimeoutSeconds*time.Second)
	defer cancel()

	dbCtx := s.buildDBTool(a.TenantID, a.UserID, "")
	tools := missionTools(dbCtx, a.SendMail)

	system := "Du er en sulten, skeptisk domene-ekspert som jobber som en gravende analytiker mot ETT mål, " +
		"runde etter runde, til det er BEVIST nådd. Du er ikke fornøyd med overflaten — du vil ha de eksakte " +
		"tallene fra primærkilden.\n" +
		"ARBEIDSDISIPLIN:\n" +
		"- Hver påstand SKAL ha et konkret tall/faktum OG en kilde. Ingen gjetting, ingen «omtrent» uten å ha " +
		"forsøkt å finne det eksakte.\n" +
		"- web_search finner kilder; gå så til PRIMÆRKILDEN med fetch_url (årsrapport, kvartalsrapport, " +
		"børsmelding, offentlig register, selskapets egne sider) og les de faktiske tallene. Ikke stol på " +
		"søke-snutter alene.\n" +
		"- Kryssjekk viktige tall mot MINST to uavhengige kilder. Spriker de, grav videre til du vet hvorfor.\n" +
		"- query_database er skrivebeskyttet — bruk den for interne data.\n" +
		"- Jobb ett konkret spor per runde, men vær utålmodig: still stadig spørsmålet «hva mangler for at en " +
		"ekspert ville stolt på dette?» og lukk det gapet.\n" +
		"SELVKRITIKK FØR DU KONKLUDERER: sett status=done KUN når HVERT fullført-kriterium er dekket med " +
		"tall + kilde, du har kryssjekket, og du selv ikke finner hull. Før done: angrip eget arbeid — hvilke " +
		"tall er antatt/uverifisert? hvilke kriterier er bare delvis dekket? Er det hull, sett status=continue " +
		"og fortsett å grave. Gi deg ALDRI på et halvferdig eller udokumentert svar.\n" +
		"Avslutt ALLTID runden med mission_update: summary = et DETALJERT funn-register (faktum + tall + kilde " +
		"per punkt, ikke et vagt sammendrag) som neste runde bygger videre på; next_steps = konkret neste spor; " +
		"status (continue/done); result + notify. IKKE post en melding hver runde: la notify stå false mens du " +
		"jobber (aktiviteten vises live). Sett notify=true (eller done) KUN ved en reell konklusjon verdt å " +
		"fortelle. result skal være tett, tallrikt og med kilder — ekspert-nivå, ikke overflate.\n" +
		"TENKING (vises live): skriv ALLTID FØR verktøykallene ÉN kort, konkret setning i førsteperson på norsk " +
		"om hva du gjør og hvorfor — f.eks. «Henter Mowi sin Q4-rapport for eksakt driftsmargin». Vær spesifikk " +
		"(navn, tall, kilde), aldri vage fraser som «jobber mot målet»."
	if a.SendMail {
		system += " Du KAN sende mail selv via send_mail når det trengs for målet."
	} else {
		system += " Du kan IKKE sende mail selv. Vil du sende en mail, legg den som mail-forslag i " +
			"mission_update, så får brukeren et ferdig kort å sende."
	}

	userMsg := "Mål:\n" + a.Task
	if strings.TrimSpace(a.MissionState) != "" {
		userMsg += "\n\nGjeldende oppdrags-tilstand (fra tidligere kjøringer):\n" + a.MissionState
	} else {
		userMsg += "\n\n(Første kjøring — ingen tidligere tilstand.)"
	}

	messages := []any{
		map[string]any{"role": "system", "content": system},
		map[string]any{"role": "user", "content": userMsg},
	}

	// Vis noe umiddelbart, så det ikke ser dødt ut mens første runde tenker.
	s.saveActivity(a.ID, "Tenker …", nil)

	var totalTokens int
	var activitySteps []string
	lastThought := "Tenker …"
	for round := 0; round <= missionMaxRounds; round++ {
		payload := map[string]any{
			"model":       router.MidModel,
			"messages":    messages,
			"stream":      false,
			"tools":       tools,
			"temperature": 0.3,
			"max_tokens":  4000,
			"reasoning":   map[string]any{"enabled": false},
		}
		body, _ := json.Marshal(payload)
		req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
		if err != nil {
			return "", totalTokens, false, false, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return "", totalTokens, false, false, err
		}
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
		dec := json.NewDecoder(resp.Body)
		derr := dec.Decode(&out)
		resp.Body.Close()
		if derr != nil {
			return "", totalTokens, false, false, derr
		}
		totalTokens += out.Usage.PromptTokens + out.Usage.CompletionTokens
		if len(out.Choices) == 0 {
			return "", totalTokens, false, false, fmt.Errorf("tomt svar fra modellen")
		}
		msg := out.Choices[0].Message

		type normCall struct{ id, name, args string }
		var norm []normCall
		for _, c := range msg.ToolCalls {
			norm = append(norm, normCall{c.ID, c.Function.Name, c.Function.Arguments})
		}
		visible, _, blocks := splitToolCallText(msg.Content)
		for i, b := range blocks {
			if name, args := parseTextualToolCall(b); name != "" {
				norm = append(norm, normCall{fmt.Sprintf("call_t%d", i), name, args})
			}
		}

		// mission_update avslutter kjøringen.
		for _, c := range norm {
			if c.name != "mission_update" {
				continue
			}
			var mu missionUpdate
			_ = json.Unmarshal([]byte(c.args), &mu)
			newState, _ := json.Marshal(map[string]string{
				"summary": mu.Summary, "next_steps": mu.NextSteps,
			})
			s.store.SetMissionState(a.ID, string(newState))

			result := strings.TrimSpace(mu.Result)
			if result == "" {
				result = strings.TrimSpace(mu.Summary)
			}
			// Mail-forslag uten send-tillatelse → ferdig kort brukeren sender.
			if mu.Mail != nil && strings.TrimSpace(mu.Mail.Email) != "" {
				result += "\n\n" + composeBlock(mu.Mail.Name, mu.Mail.Email, mu.Mail.Subject, mu.Mail.Body)
			}
			done := strings.EqualFold(strings.TrimSpace(mu.Status), "done")
			if done {
				result += "\n\n_Oppdrag fullført._"
			}
			return strings.TrimSpace(result), totalTokens, done, mu.Notify || done, nil
		}

		// Ingen verktøykall og ingen mission_update → runden ga bare narrasjon.
		// Vis den som aktivitet, men ikke post noe (ingen konklusjon).
		if len(norm) == 0 {
			if t := strings.TrimSpace(visible); t != "" {
				lastThought = t
				s.saveActivity(a.ID, lastThought, activitySteps)
			}
			return strings.TrimSpace(visible), totalTokens, false, false, nil
		}

		if a.DailyTokenLimit > 0 && totalTokens >= a.DailyTokenLimit {
			return strings.TrimSpace(visible), totalTokens, false, false, nil
		}

		// Live-aktivitet: hva agenten tenker + verktøyene den bruker denne runden.
		for _, c := range norm {
			if lbl := toolStepLabel(c.name, c.args); lbl != "" &&
				(len(activitySteps) == 0 || activitySteps[len(activitySteps)-1] != lbl) {
				activitySteps = append(activitySteps, lbl)
			}
		}
		if len(activitySteps) > 12 {
			activitySteps = activitySteps[len(activitySteps)-12:]
		}
		if t := strings.TrimSpace(visible); t != "" {
			lastThought = t
		}
		s.saveActivity(a.ID, lastThought, activitySteps)

		// Utfør verktøykallene og mat resultatene tilbake.
		calls := make([]any, 0, len(norm))
		toolMsgs := make([]any, 0, len(norm))
		for _, c := range norm {
			calls = append(calls, map[string]any{
				"id": c.id, "type": "function",
				"function": map[string]any{"name": c.name, "arguments": c.args},
			})
			var args struct {
				Query        string `json:"query"`
				URL          string `json:"url"`
				ConnectionID string `json:"connection_id"`
				SQL          string `json:"sql"`
				ToName       string `json:"to_name"`
				ToEmail      string `json:"to_email"`
				Subject      string `json:"subject"`
				Body         string `json:"body"`
			}
			_ = json.Unmarshal([]byte(c.args), &args)
			var result string
			switch c.name {
			case "query_database":
				result = s.runDBQuery(ctx, dbCtx, args.ConnectionID, args.SQL)
			case "fetch_url":
				result = s.runFetchURL(ctx, args.URL)
			case "send_mail":
				if err := s.sendAgentMail(a, args.ToName, args.ToEmail, args.Subject, args.Body); err != nil {
					result = "Kunne ikke sende: " + err.Error()
				} else {
					result = "Sendt til " + args.ToEmail
				}
			default:
				result, _ = s.runWebSearch(ctx, args.Query)
			}
			toolMsgs = append(toolMsgs, map[string]any{
				"role": "tool", "tool_call_id": c.id, "content": result,
			})
		}
		messages = append(messages, map[string]any{
			"role": "assistant", "content": "", "tool_calls": calls,
		})
		messages = append(messages, toolMsgs...)
	}
	// Nådde runde-taket uten mission_update — behold tilstanden, fortsett neste gang.
	return "", totalTokens, false, false, nil
}

// toolStepLabel gir en kort, brukervennlig etikett for et verktøykall (samme
// stil som stegene i hovedchatten).
func toolStepLabel(name, rawArgs string) string {
	switch name {
	case "query_database":
		return "Spør databasen"
	case "send_mail":
		return "Sender e-post"
	case "fetch_url":
		var a struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &a)
		if u := strings.TrimSpace(a.URL); u != "" {
			return "Leser: " + u
		}
		return "Leser en side"
	case "web_search":
		var a struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &a)
		if q := strings.TrimSpace(a.Query); q != "" {
			return "Søker: " + q
		}
		return "Søker på nettet"
	default:
		return ""
	}
}

// saveActivity lagrer den live «hva jeg gjør/tenker»-tilstanden på agenten.
func (s *Server) saveActivity(agentID, thought string, steps []string) {
	payload, _ := json.Marshal(map[string]any{"thought": thought, "steps": steps})
	s.store.SetMissionActivity(agentID, string(payload))
}

// sendAgentMail sender en e-post på vegne av agentens eier via den delte kontoen.
func (s *Server) sendAgentMail(a store.Agent, name, email, subject, body string) error {
	user, err := s.store.UserByID(a.UserID)
	if err != nil {
		return err
	}
	acc, rec, err := s.mailAccount(user)
	if err != nil {
		return fmt.Errorf("ingen e-postkonto tilgjengelig")
	}
	b := body
	if strings.TrimSpace(rec.Signature) != "" {
		b = b + "\n\n" + rec.Signature
	}
	return acc.Send(mail.OutMessage{
		To:      []mail.Person{{Name: name, Address: email}},
		Subject: subject,
		Body:    b,
	})
}
