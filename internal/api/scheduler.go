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
	schedulerTick           = 30 * time.Second
	agentMaxRounds          = 6
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
	var dbCtx *dbToolCtx
	if a.ConnectionID != "" {
		dbCtx = s.buildDBTool(a.TenantID, a.UserID, a.ConnectionID)
		if dbCtx != nil {
			tools = append(tools, dbCtx.tool)
		}
	}

	system := "Du er en autonom agent. Systemet håndterer tidsplan og frekvens — du skal utføre " +
		"oppgaven ÉN gang nå. Ignorer ord som «hvert minutt/daglig» i oppgaven; det styrer bare " +
		"hvor ofte du kjøres. Si ALDRI at du ikke kan kjøre automatisk, planlagt eller kontinuerlig — " +
		"bare lever resultatet direkte. Kort, konkret svar på norsk, ingen innledning eller spørsmål tilbake. Kun lesing" +
		func() string {
			if a.WriteAccess {
				return "; skriving er tillatt hvis oppgaven krever det."
			}
			return " — aldri endre data."
		}()

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

		if len(msg.ToolCalls) == 0 {
			return strings.TrimSpace(msg.Content), totalTokens, nil
		}

		// Stopp hvis token-grensen er passert underveis.
		if a.DailyTokenLimit > 0 && totalTokens >= a.DailyTokenLimit {
			return strings.TrimSpace(msg.Content), totalTokens, nil
		}

		// Registrer assistant-meldingen med verktøykallene og utfør dem.
		calls := make([]any, 0, len(msg.ToolCalls))
		toolMsgs := make([]any, 0, len(msg.ToolCalls))
		for _, c := range msg.ToolCalls {
			calls = append(calls, map[string]any{
				"id": c.ID, "type": "function",
				"function": map[string]any{"name": c.Function.Name, "arguments": c.Function.Arguments},
			})
			var args struct {
				Query        string `json:"query"`
				ConnectionID string `json:"connection_id"`
				SQL          string `json:"sql"`
			}
			_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
			var result string
			if c.Function.Name == "query_database" {
				result = s.runDBQuery(ctx, dbCtx, args.ConnectionID, args.SQL)
			} else {
				result, _ = s.runWebSearch(ctx, args.Query)
			}
			toolMsgs = append(toolMsgs, map[string]any{
				"role": "tool", "tool_call_id": c.ID, "content": result,
			})
		}
		messages = append(messages, map[string]any{
			"role": "assistant", "content": "", "tool_calls": calls,
		})
		messages = append(messages, toolMsgs...)
	}
	return "", totalTokens, fmt.Errorf("nådde maks antall verktøyrunder uten svar")
}
