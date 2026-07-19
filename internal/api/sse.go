package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// toolCall akkumulerer et verktøykall som strømmer inn i biter over SSE.
type toolCall struct {
	ID   string
	Name string
	Args strings.Builder
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
