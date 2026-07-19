package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/router"
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

type toolCall struct {
	ID   string
	Name string
	Args strings.Builder
}

// runAgentLoop kjører chat-forespørselen med web_search som verktøy.
// Reasoning og modell-chunks streames til klienten underveis; når modellen
// begynner på selve svaret, pipes resten rått gjennom.
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
		if widgetSlug != "" {
			tools = append(tools, widgetTools()...)
		}
		full["tools"] = tools
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

// roundUsage er tokenforbruket rapportert i én streaming-runde.
type roundUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// relayRound leser én SSE-runde fra upstream. Innhold, reasoning og
// modellinfo videresendes til klienten; tool_calls samles og returneres
// (aldri videresendt). Upstreams [DONE] svelges — løkka eier avslutningen.
func (s *Server) relayRound(resp *http.Response, emit func(string)) (map[int]*toolCall, roundUsage) {
	calls := map[int]*toolCall{}
	var usage roundUsage

	// Noen modeller emitterer verktøykall som ren tekst
	// (<tool_call>{...}</tool_call>) i innholdet i stedet for som strukturerte
	// tool_calls. Vi bufrer innhold, stripper slike blokker før de når
	// klienten, og gjør dem om til ekte kall. textIdx holder tekstlige kall
	// unna de strukturertes indekser.
	var pending strings.Builder
	textIdx := 1000

	for scanner := newSSEScanner(resp.Body); scanner.Scan(); {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Usage   *roundUsage `json:"usage"`
			Model   string      `json:"model"`
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil || len(chunk.Choices) == 0 {
			if err == nil && chunk.Usage != nil {
				usage = *chunk.Usage
			}
			emit(line)
			continue
		}

		if tcs := chunk.Choices[0].Delta.ToolCalls; len(tcs) > 0 {
			for _, tc := range tcs {
				c, ok := calls[tc.Index]
				if !ok {
					c = &toolCall{}
					calls[tc.Index] = c
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				if tc.Function.Name != "" {
					c.Name = tc.Function.Name
				}
				c.Args.WriteString(tc.Function.Arguments)
			}
			continue
		}

		if content := chunk.Choices[0].Delta.Content; content != "" {
			pending.WriteString(content)
			safe, rest, blocks := splitToolCallText(pending.String())
			pending.Reset()
			pending.WriteString(rest)
			for _, b := range blocks {
				if name, args := parseTextualToolCall(b); name != "" {
					c := &toolCall{ID: fmt.Sprintf("call_t%d", textIdx), Name: name}
					c.Args.WriteString(args)
					calls[textIdx] = c
					textIdx++
				}
			}
			// Uendret innhold: send originallinja (bevarer model o.l.). Ellers
			// syntetiser en innholds-chunk for den trygge delen.
			if safe == content && rest == "" {
				emit(line)
			} else if safe != "" {
				emit(contentSSE(safe))
			}
			continue
		}
		// Reasoning, modellinfo og annet går rett til klienten.
		emit(line)
	}
	// Uavsluttet buffer (aldri lukket tag): send det som innhold så ingenting mistes.
	if pending.Len() > 0 {
		emit(contentSSE(pending.String()))
	}
	return calls, usage
}

func newSSEScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return sc
}

const tcOpen = "<tool_call>"
const tcClose = "</tool_call>"

// splitToolCallText deler bufret innhold i (trygt innhold å sende, rest å
// holde igjen, komplette <tool_call>-blokker uten tagger). Resten holdes
// igjen når den kan være starten på en ny (u)lukket tag.
func splitToolCallText(buf string) (safe, rest string, blocks []string) {
	var out strings.Builder
	for {
		i := strings.Index(buf, tcOpen)
		if i < 0 {
			// Ingen åpningstag: send alt bortsett fra en mulig delvis tag-hale.
			keep := partialSuffix(buf, tcOpen)
			out.WriteString(buf[:len(buf)-keep])
			return out.String(), buf[len(buf)-keep:], blocks
		}
		out.WriteString(buf[:i])
		after := buf[i:]
		j := strings.Index(after, tcClose)
		if j < 0 {
			// Åpnet men ikke lukket ennå: hold resten igjen.
			return out.String(), after, blocks
		}
		blocks = append(blocks, after[len(tcOpen):j])
		buf = after[j+len(tcClose):]
	}
}

// partialSuffix gir lengden på den lengste halen av s som er et prefiks av tag.
func partialSuffix(s, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for k := max; k > 0; k-- {
		if strings.HasSuffix(s, tag[:k]) {
			return k
		}
	}
	return 0
}

// parseTextualToolCall tolker innholdet i en <tool_call>-blokk: {"name":...,
// "arguments":{...}}. Returnerer navn og argumentene som JSON-streng.
func parseTextualToolCall(body string) (name, args string) {
	var tc struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &tc); err != nil {
		return "", ""
	}
	args = string(tc.Arguments)
	// Argumentene kan komme som en JSON-streng i stedet for objekt.
	if len(args) > 0 && args[0] == '"' {
		var inner string
		if json.Unmarshal(tc.Arguments, &inner) == nil {
			args = inner
		}
	}
	return tc.Name, args
}

// contentSSE bygger en SSE-innholds-chunk klienten forstår.
func contentSSE(text string) string {
	payload, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": text}}},
	})
	return "data: " + string(payload)
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
	payload := map[string]any{
		"model": router.MidModel,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": "Lag en kort, forklarende tittel på norsk for samtalen, 3-5 ord. " +
					"Svar KUN med tittelen — ingen anførselstegn eller punktum.",
			},
			{"role": "user", "content": "Spørsmål: " + question + "\n\nSvar: " + answer},
		},
		"max_tokens":  25,
		"temperature": 0.3,
		"reasoning":   map[string]any{"enabled": false},
	}
	body, _ := json.Marshal(payload)

	req, err := s.newUpstreamRequest(r.Context(), bytes.NewReader(body))
	if err != nil {
		return ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return ""
	}
	title := strings.Trim(strings.TrimSpace(out.Choices[0].Message.Content), `"'.«»`)
	if t := []rune(title); len(t) > 60 {
		title = string(t[:60])
	}
	return title
}
