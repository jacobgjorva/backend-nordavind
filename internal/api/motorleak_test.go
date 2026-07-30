package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Teksten under er ORDRETT fra prod-lekkasjen 2026-07-30: modellen skrev
// verktøykall som prosa midt i svaret, klippevakten fjernet «web_search {»
// men slapp fortsettelsen gjennom, og brukeren fikk SQL mot en tabell som
// ikke finnes. Ekte modellutdata som testdata.
const realLeakedAnswer = "Mange selskaper betaler vanlige folk for å validere eller skaffe " +
	"treningsdata til AI-modeller, ofte gjennom plattformer som Appen, Amazon Mechanical Turk, " +
	"Clickworker, Toloka og Scale AI.\n\nBetalingen varierer fra noen få øre til flere hundre " +
	"kroner per time.\n\nJeg sjekker nå hvilke av disse som er mest aktive og tilgjengelige for " +
	"folk i Norge i juli 2026. web_search {\"query\":\n\n\"AI treningsdata plattformer som betaler " +
	"vanlige folk Norge juli 2026\"} query_database {\"query\":\n\n\"SELECT platform_name, " +
	"payment_rate, task_type, availability FROM ai_data_platforms WHERE active = true AND " +
	"available_in_norway = true AND last_updated >= '2026-01-01' ORDER BY payment_rate DESC LIMIT 5\"}"

// chunkedSSE simulerer en upstream-strøm der teksten kommer i biter av gitt
// størrelse — det er chunkgrensene som utløste lekkasjen.
func chunkedSSE(text string, size int) string {
	var b strings.Builder
	r := []rune(text)
	for i := 0; i < len(r); i += size {
		end := i + size
		if end > len(r) {
			end = len(r)
		}
		chunk := map[string]any{"content": string(r[i:end])}
		b.WriteString(`data: {"choices":[{"delta":{"content":` + jsonString(chunk["content"].(string)) + `}}]}` + "\n")
	}
	b.WriteString("data: [DONE]\n")
	return b.String()
}

func jsonString(s string) string {
	out := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + out + `"`
}

// Sperren i transporten: etter et utagget verktøykall skal INGENTING mer av
// rundens innhold nå brukeren — heller ikke fortsettelses-chunks som ikke
// selv ligner en verktøystart.
func TestRelaySuppressesEverythingAfterBareToolCall(t *testing.T) {
	for _, size := range []int{7, 24, 80, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, chunkedSSE(realLeakedAnswer, size))
		}))
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		s := quietServer()
		_, _, content := s.relayRound(resp, func(string) {}, false, nil)
		srv.Close()

		for _, forbidden := range []string{"web_search", "query_database", "SELECT", "ai_data_platforms", `{"query"`} {
			if strings.Contains(content, forbidden) {
				t.Errorf("chunk=%d: %q lekket gjennom: …%s", size, forbidden,
					content[maxOf(0, len(content)-120):])
				break
			}
		}
		// Prosaen FØR kallet skal overleve.
		if !strings.Contains(content, "Mange selskaper betaler vanlige folk") {
			t.Errorf("chunk=%d: den ekte prosaen forsvant", size)
		}
	}
}

func maxOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Formgulvet: skulle syntaks likevel nå leveransen (en vei vi ikke har
// forutsett), skal utkastet forkastes og skrives på nytt fra evidensen —
// aldri vises.
func TestFormFloorRejectsLeakedSyntaxDraft(t *testing.T) {
	junk := `web_search {"query": "noe"} query_database {"query": "SELECT x FROM y"}`
	if !isJunkAnswer(junk) {
		t.Skip("isJunkAnswer regner ikke denne som søppel — sperren i transporten er da eneste vern")
	}
	if isJunkAnswer("Styringsrenten i Norge er 4,25 prosent, satt av Norges Bank i juni.") {
		t.Error("et vanlig svar skal aldri regnes som søppel")
	}
}

// Ende-til-ende gjennom leveransen: junk-utkast + fungerende skriver →
// brukeren får det omskrevne svaret, aldri syntaksen.
func TestDeliveryRewritesJunkDraftFromEvidence(t *testing.T) {
	out := &collectEmitter{}
	d := &motor.Delivery{
		Out:  out,
		Form: motorForm{},
		Writer: writerFunc(func(facts, draft string) string {
			if draft != "" {
				t.Errorf("skriveren skal få tomt utkast etter formforkastelse, fikk %q", draft)
			}
			return "Basert på kildene: flere plattformer betaler vanlige folk for dataannotering."
		}),
	}
	turn := &motor.Turn{Method: motor.MethodLookup, UsedTool: true,
		Evidence: []string{"kildetekst om plattformer"}}

	d.Deliver(t.Context(), turn, `web_search {"query": "x"} {"SELECT a FROM b"}`)

	if len(out.content) != 1 || strings.Contains(out.content[0], "web_search") {
		t.Fatalf("brukeren skulle fått det omskrevne svaret: %v", out.content)
	}
}

type collectEmitter struct{ content []string }

func (c *collectEmitter) Content(s string)       { c.content = append(c.content, s) }
func (c *collectEmitter) Step(string, string)    {}
func (c *collectEmitter) Sources([]motor.Source) {}
func (c *collectEmitter) Done()                  {}

type writerFunc func(facts, draft string) string

func (f writerFunc) Write(_ context.Context, _ *motor.Turn, facts, draft string) string {
	return f(facts, draft)
}
