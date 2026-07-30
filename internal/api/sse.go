package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode"
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
// relayRound streamer/bufrer én upstream-runde. streamContent=false bufrer
// svar-innholdet i stedet for å sende det live (til den tvungne siste runden,
// der vi vil se om svaret ble tynt før vi viser noe). Med gate != nil går alt
// synlig innhold gjennom setnings-gaten (kildekontroll på streamede svar) —
// kalleren eier gate.finish() og det tilbakeholdte. Returnerer verktøykall,
// tokenbruk og hele det synlige svar-innholdet.
func (s *Server) relayRound(resp *http.Response, emit func(string), streamContent bool, gate *sentenceGate) (map[int]*toolCall, roundUsage, string) {
	calls := map[int]*toolCall{}
	var usage roundUsage
	var visible strings.Builder // hele synlige svar-innholdet denne runden

	// Noen modeller emitterer verktøykall som ren tekst
	// (<tool_call>{...}</tool_call>) i innholdet i stedet for som strukturerte
	// tool_calls. Vi bufrer innhold, stripper slike blokker før de når
	// klienten, og gjør dem om til ekte kall. textIdx holder tekstlige kall
	// unna de strukturertes indekser.
	var pending strings.Builder
	textIdx := 1000
	// suppressed: et utagget verktøykall er oppdaget — resten av rundens
	// innhold er syntaks og skal aldri til brukeren.
	suppressed := false

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
			// Etter et utagget verktøykall er RESTEN av runden syntaks.
			// Uten sperren lakk fortsettelsen: klippet fjernet
			// «query_database {» fra ett chunk, men neste chunk («"SELECT
			// … FROM …"}») lignet ingen verktøystart og gikk rett til
			// brukeren (målt i prod 2026-07-30).
			if suppressed {
				continue
			}
			pending.WriteString(content)
			safe, rest, blocks := splitToolCallText(pending.String())
			pending.Reset()
			pending.WriteString(rest)
			// Utagget verktøykall midt i prosaen: klipp det og alt etter.
			if cut := cutBareToolCall(safe); cut != safe {
				safe = cut
				pending.Reset()
				suppressed = true
			}
			visible.WriteString(safe)
			for _, b := range blocks {
				if name, args := parseTextualToolCall(b); name != "" {
					c := &toolCall{ID: fmt.Sprintf("call_t%d", textIdx), Name: name}
					c.Args.WriteString(args)
					calls[textIdx] = c
					textIdx++
				}
			}
			// Bufret runde: ikke send innholdet live (kalleren avgjør etterpå).
			if !streamContent {
				continue
			}
			// Gatet runde: gaten avgjør hva som er trygt å vise nå.
			if gate != nil {
				if rel := gate.feed(safe); rel != "" {
					emit(contentSSE(rel))
				}
				continue
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
	if pending.Len() > 0 && !suppressed {
		visible.WriteString(pending.String())
		if streamContent {
			if gate != nil {
				if rel := gate.feed(pending.String()); rel != "" {
					emit(contentSSE(rel))
				}
			} else {
				emit(contentSSE(pending.String()))
			}
		}
	}
	return calls, usage, visible.String()
}

func newSSEScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return sc
}

const tcOpen = "<tool_call>"
const tcClose = "</tool_call>"

// Verktøykall uten tagger: småmodellene skriver av og til kallet rått inn i
// prosaen («… var «tekoligark»fetch_url{"url": …»). Mønsteret verktøynavn
// umiddelbart etterfulgt av { finnes aldri i naturlig norsk tekst, så alt
// fra og med treffet klippes bort — resten av svaret er uansett tapt.
var bareToolCallRe = regexp.MustCompile(
	`(web_search|fetch_url|query_database|show_table|m365_search|m365_read|` +
		`mail_search|mail_read|mail_compose|set_widget|compose|patch|restyle|` +
		`contact_person|list_agents|update_agent|setup_routine)\s*\{`)

// cutBareToolCall klipper et utagget verktøykall og alt etter det.
func cutBareToolCall(s string) string {
	if loc := bareToolCallRe.FindStringIndex(s); loc != nil {
		return strings.TrimRight(s[:loc[0]], " \t\n")
	}
	return s
}

var toolNames = []string{
	"web_search", "fetch_url", "query_database", "show_table", "m365_search",
	"m365_read", "mail_search", "mail_read", "mail_compose", "set_widget",
	"compose", "patch", "restyle", "contact_person", "list_agents", "update_agent",
	"setup_routine",
}

// bareToolCallPrefix gir lengden på halen av s som kan være starten på et
// utagget verktøykall — den holdes igjen til neste chunk avgjør saken.
func bareToolCallPrefix(s string) int {
	best := 0
	for _, name := range toolNames {
		for k := len(name); k > 0; k-- {
			if k <= best {
				break
			}
			if strings.HasSuffix(s, name[:k]) {
				best = k
				break
			}
		}
	}
	return best
}

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
			// … og en mulig begynnelse på et utagget verktøykall («…fetch_ur»),
			// som ellers ville rukket å bli streamet før { avslørte den.
			if k := bareToolCallPrefix(buf[:len(buf)-keep]); k > keep {
				keep = k
			}
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

// jsonFragRe: en JSON-nøkkel med verdi («"query": "…"») i løpende prosa er
// alltid et lekket verktøykall-fragment, aldri et svar.
var jsonFragRe = regexp.MustCompile(`[a-zA-Z_]+"\s*:\s*["{\[]`)

// fenceRe fjerner kodeblokker før formsjekken — tabell- og widgetblokker er
// legitim JSON i svaret.
var fenceRe = regexp.MustCompile("(?s)```.*?```")

// isJunkAnswer: er svaret åpenbart ødelagt struktur — lekket verktøykall-JSON
// eller tegnsøppel uten språk? Formsjekk på FAKTA (tegnklasser og struktur),
// aldri på mening. Målt: «ermannquery": "Brent Crude …"}» og «…………」» gikk
// rett til brukeren; ingen av gatene måler form.
func isJunkAnswer(s string) bool {
	s = strings.TrimSpace(fenceRe.ReplaceAllString(s, ""))
	if s == "" {
		return false // helt tomt håndteres av tom-vernet, ikke her
	}
	if jsonFragRe.MatchString(s) {
		return true
	}
	// Tegnsøppel: nesten ingen bokstaver i noe som skal være en setning.
	letters, total := 0, 0
	hasDigit := false
	for _, r := range s {
		total++
		if unicode.IsLetter(r) {
			letters++
		}
		if unicode.IsDigit(r) {
			hasDigit = true
		}
	}
	if total >= 5 && letters*10 < total*3 {
		return true
	}
	// Bar ordstump: ett enslig «ord» uten setningsslutt og uten tall er en
	// degenereringshale («eskola», «lium»), aldri et svar. Ekte kortsvar har
	// tegnsetting («Ja.») eller tall («426 ordre.»).
	if !hasDigit && !strings.ContainsAny(s, ".!?") &&
		len(strings.Fields(s)) == 1 && len([]rune(s)) <= 14 {
		return true
	}
	return false
}

// stripForeignHead fjerner et degenerert ledetoken i FREMMED skrift («جيكا Det
// finnes …», «нымі …») foran en ellers latinsk setning. Måler skriftsystem,
// aldri innhold: tokenet må være rene bokstaver uten ett eneste latinsk tegn,
// og resten av teksten må være overveiende latinsk. Norske svar starter aldri
// legitimt med et helarabisk eller helkyrillisk ord.
func stripForeignHead(s string) string {
	trimmed := strings.TrimLeft(s, " ")
	i := strings.IndexAny(trimmed, " \n\t")
	if i <= 0 {
		return s
	}
	head, rest := trimmed[:i], strings.TrimLeft(trimmed[i:], " ")
	if rest == "" {
		return s
	}
	latin, letters := 0, 0
	for _, r := range head {
		if !unicode.IsLetter(r) {
			return s // tegnsetting/tall i hodet: ikke vår klasse
		}
		letters++
		if unicode.In(r, unicode.Latin) {
			latin++
		}
	}
	if letters == 0 || latin > 0 {
		return s
	}
	// Resten må være overveiende latinsk — ellers er hele svaret på et annet
	// språk, og det skal ikke røres her.
	rl, rt := 0, 0
	for _, r := range rest {
		if unicode.IsLetter(r) {
			rt++
			if unicode.In(r, unicode.Latin) {
				rl++
			}
		}
	}
	if rt == 0 || rl*10 < rt*8 {
		return s
	}
	return rest
}
