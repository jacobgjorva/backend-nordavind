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

const maxToolRounds = 3

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

	// Agent-chat: brukeren kan be agenten om å endre seg selv.
	editID, _ := full["nordavind_agent_edit"].(string)
	delete(full, "nordavind_agent_edit")
	editable := false
	if editID != "" {
		if user, ok := ctx.Value(userKey).(store.User); ok {
			if a, err := s.store.GetAgent(editID, user.ID); err == nil {
				injectSystem(full, agentEditSystem(a))
				editable = true
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
		} else {
			tools := []any{webSearchTool}
			if dbCtx != nil {
				tools = append(tools, dbCtx.tool)
			}
			// Agent-verktøy kun i /agent-modus.
			if setup {
				tools = append(tools, s.buildAgentTools(ctx)...)
			}
			if editable {
				tools = append(tools, buildAgentEditTools()...)
			}
			full["tools"] = tools
		}
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
	defer func() {
		s.recordUsage(ctx, full, promptTokens, completionTokens, searches, time.Since(start))
	}()

	for round := 0; round <= maxToolRounds; round++ {
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

		calls, usage := s.relayRound(resp, emit)
		resp.Body.Close()
		promptTokens += usage.PromptTokens
		completionTokens += usage.CompletionTokens
		if len(calls) == 0 {
			// Svaret er streamet ferdig til klienten.
			emit("data: [DONE]")
			return
		}

		// Utfør verktøykallene og legg resultatene inn i samtalen.
		assistantCalls := make([]any, 0, len(calls))
		toolMsgs := make([]any, 0, len(calls))
		for _, c := range calls {
			var args struct {
				Query        string `json:"query"`
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

		if round == maxToolRounds-1 {
			// Siste runde: tving frem et svar uten flere verktøykall.
			full["tool_choice"] = "none"
		}
	}
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
	pages := s.search.FetchPages(ctx, results, 3000)
	s.log.Info("websøk", "query", query, "treff", len(results))

	refs := make([]sourceRef, 0, len(results))
	for _, r := range results {
		refs = append(refs, sourceRef{Title: r.Title, URL: r.URL})
	}
	return search.FormatContext(query, results, pages), refs
}

// generateTitle ber standardmodellen om en kort samtaletittel.
func (s *Server) generateTitle(r *http.Request, question, answer string) string {
	if a := []rune(answer); len(a) > 500 {
		answer = string(a[:500])
	}
	system := "Lag en kort, forklarende tittel på norsk for samtalen, 3-5 ord. " +
		"Svar KUN med tittelen — ingen anførselstegn eller punktum."
	raw, err := s.llmComplete(r.Context(), system, "Spørsmål: "+question+"\n\nSvar: "+answer)
	if err != nil {
		return ""
	}
	title := strings.Trim(strings.TrimSpace(raw), `"'.«»`)
	if t := []rune(title); len(t) > 60 {
		title = string(t[:60])
	}
	return title
}
