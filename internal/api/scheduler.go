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
	// Spinup-jobber som ble avbrutt av en omstart står som «building» uten at
	// noen jobber — nullstill og bygg dem på nytt, ellers henger de for alltid.
	if ids, err := s.store.StuckPlanAgents(); err == nil {
		for _, id := range ids {
			s.log.Info("gjenopptar avbrutt spinup", "agent", id)
			s.store.SetPlanStatus(id, "", "")
			s.store.SetMissionActivity(id, "")
			s.startPlanBuild(id)
		}
	}

	// Live OneDrive-eksporter: push ferske tall inn i arbeidsbøkene fast.
	pushTicker := time.NewTicker(15 * time.Minute)
	go func() {
		defer pushTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pushTicker.C:
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
	// Marker kjøringen for live-tilstanden (trollet «skriver» i farmen).
	s.runMu.Lock()
	s.runActive[a.ID] = true
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		delete(s.runActive, a.ID)
		s.runMu.Unlock()
	}()

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

	// Har agenten en ferdig kompilert plan, kjøres den deterministisk — billig
	// og med samme tall hver gang. Uten plan (eller hvis den nettopp brakk)
	// faller vi tilbake til fri kjøring, så agenten alltid gjør nytte.
	var (
		run     planRun
		planned bool
		err     error
	)
	if a.PlanStatus == "ready" && strings.TrimSpace(a.Plan) != "" {
		run, err = s.executeAgentPlan(ctx, a)
		planned = err == nil
		if err != nil {
			s.log.Warn("plan-kjøring feilet, faller tilbake til fri kjøring", "id", a.ID, "err", err)
			run.output, run.tokens, err = s.executeAgent(ctx, a)
		}
	} else {
		run.output, run.tokens, err = s.executeAgent(ctx, a)
		// Ingen plan ennå: bygg den i bakgrunnen, så neste kjøring blir bedre.
		if a.PlanStatus == "" {
			s.startPlanBuild(a.ID)
		}
	}
	if err != nil {
		s.log.Warn("agent-kjøring feilet", "id", a.ID, "err", err)
		s.store.RecordRun(a.ID, store.AgentRun{Status: "error", TokensUsed: run.tokens, Error: err.Error()})
		s.postToAgentChat(a, "Kjøringen feilet: "+err.Error())
		return
	}

	// Ingenting har endret seg siden sist: logg det, men ikke mas i chatten.
	if run.unchanged {
		s.store.RecordRun(a.ID, store.AgentRun{Status: "unchanged"})
		s.log.Info("agent kjørt, ingen endring", "id", a.ID, "navn", a.Name)
		return
	}

	// Alert («Funn!» i grafen): KUN plan-kjøringer, der agentens egen
	// varselregel har avgjort det. Frie kjøringer har ingen pålitelig
	// funn-signal — et postet resultat kan like gjerne være «ingenting nytt»,
	// og en falsk bjelle er verre enn en manglende.
	alert := planned && run.alert
	s.store.RecordRun(a.ID, store.AgentRun{Status: "ok", Output: run.output, TokensUsed: run.tokens, Alert: alert})
	s.postToAgentChat(a, run.output)
	s.log.Info("agent kjørt", "id", a.ID, "navn", a.Name, "tokens", run.tokens, "varsel", alert)

	if !a.PushEnabled {
		return
	}
	// Med plan har tolkningen allerede avgjort mot agentens egen varselregel —
	// da trengs ingen ekstra klassifisering. Uten plan må vi spørre.
	if planned {
		if run.alert {
			go s.pushNow(a, run.output)
		}
		return
	}
	go s.maybePush(a, run.output)
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
	raw, err := s.llmComplete(ctx, "rutine", pushClassifySystem, head, 3)
	if err != nil || !strings.Contains(strings.ToLower(raw), "ja") {
		return
	}
	s.pushNow(a, body)
}

// pushNow køer varselet uten å vurdere det på nytt — brukes når avgjørelsen
// allerede er tatt (agentens egen varselregel).
func (s *Server) pushNow(a store.Agent, output string) {
	body := strings.TrimSpace(output)
	if body == "" {
		return
	}
	if len(body) > 200 {
		body = body[:200]
	}
	if err := s.store.EnqueuePush(a.TenantID, a.UserID, a.ID, a.Name, body); err != nil {
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

	tools := []any{webSearchTool, agentSendMailTool}
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
		"Kun lesing — databasen er skrivebeskyttet, du kan aldri endre data. " +
		"Ber oppgaven eksplisitt om at det sendes e-post: send den selv med send_mail — du har " +
		"fullmakt. Ellers skal du ALDRI sende e-post."
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
			// TOKEN: katalogen kan ikke brukes med "none" — ikke betal for den.
			delete(payload, "tools")
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
		// Rutinekjøringer eies av agentens bruker — synlige i /forbruk.
		s.countLLMFor(a.TenantID, a.UserID, router.MidModel, "rutine",
			out.Usage.PromptTokens, out.Usage.CompletionTokens)
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
				ToName       string `json:"to_name"`
				ToEmail      string `json:"to_email"`
				Subject      string `json:"subject"`
				Body         string `json:"body"`
			}
			_ = json.Unmarshal([]byte(c.args), &args)
			var result string
			switch c.name {
			case "query_database":
				result = s.runDBQuery(ctx, dbCtx, args.ConnectionID, args.SQL).text
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
	return "", totalTokens, fmt.Errorf("nådde maks antall verktøyrunder uten svar")
}

// agentSendMailTool: agenter er UNNTAKET fra chat-regelen om at brukeren selv
// må sende mail — en planlagt agent har stående fullmakt og sender selv.
var agentSendMailTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "send_mail",
		"description": "Send en e-post på brukerens vegne. Bruk KUN når oppgaven eksplisitt ber om " +
			"e-post, og til mottakeren oppgaven nevner.",
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
