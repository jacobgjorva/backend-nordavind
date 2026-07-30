// v6probe kjører designprobene for motor v6 (docs/MOTOR-V6.md, del 9) mot
// den FAKTISKE modellen med EKTE websøk. Ingen mocks — transkriptene er
// bevismaterialet og arkiveres i sin helhet.
//
// KOSTNAD: hver prompt er ekte modellkall + søk. Kjøres kun i avtalte prober.
//
//	go run ./cmd/v6probe -set cmd/v6probe/testdata/dev.jsonl \
//	  -think -method auto -out probe_runs
//
// Flagg:
//
//	-set     promptsett (jsonl: {id, class, text})
//	-think   legg på tenk-så-handle-instruksen (P1)
//	-method  "" (naken) | "auto" (klassens metodeblokk) | konkret nøkkel (P3)
//	-reason  "" (utelat feltet) | "on" | "off"  (P2)
//	-only    kjør bare én id
//	-rounds  maks verktøyrunder (default 6)
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

// Baseline-systemprompt: ordrett kopi av prod-reglene (basesystem.go) slik at
// variant A måler DAGENS adferd, ikke en stråmann.
const baseVoice = "Svar KORTEST MULIG, men alltid i hele, naturlige setninger — aldri telegramstil eller " +
	"ettordssvar. Ingen innramming, kontekst eller tomme forbehold. Trengs substans: " +
	"legg det viktigste i FØRSTE setning, si hvert poeng bare ÉN gang, og STOPP straks verdien er " +
	"levert. Kun løpende tekst, aldri overskrifter eller lister. " +
	"Ved råd: land én tydelig anbefaling. Kun hvis forespørselen er for " +
	"vag: still ett oppklarende spørsmål som navngir det konkrete du mangler. " +
	"Tone: avslappet og lun som en trygg kollega."

const baseSearch = " GJETT ALDRI på fakta. For ENHVER konkret opplysning om " +
	"virkeligheten — navn, tall, datoer, priser, statistikk, hendelser — " +
	"skal du anta at du IKKE vet det sikkert og bruke web_search FØR du svarer. " +
	"Dekker ikke kildene svaret, si det heller enn å gjette. Skriv aldri URL-er."

const baseExecute = " UTFØR, ALDRI ANNONSER. Har du verktøy tilgjengelig, bruk dem i SAMME svar " +
	"— aldri beskriv hva du «vil gjøre», aldri be brukeren om data du kan hente selv, aldri lever " +
	"en steg-for-steg-plan. Først når verktøyene har svart, skriver du svaret."

// Tenk-så-handle (P1): tanken skal komme som kort prosa I SAMME svar som
// verktøykallene — den blir narrasjon og relevansanker i v6.
const thinkRule = " Før verktøykallene: tenk kort høyt i én til tre setninger — hva brukeren egentlig " +
	"trenger, hva du alt vet, og hva du må sjekke. Skriv tanken som vanlig tekst og kall verktøyene " +
	"i SAMME svar. Tanken er arbeidsnotat, ikke svaret."

// Metodekatalogen (docs/MOTOR-V6.md del 6) — utkastene som probes i P3.
var methods = map[string]string{
	"oppslag": " METODE: Dette er et faktaoppslag. Verifiser med ett søk selv om du tror du vet svaret — " +
		"ferskhet slår hukommelse. Én autoritativ kilde holder. Svar med faktumet og tidspunktet det " +
		"gjelder for. Ikke utred.",
	"relasjon": " METODE: Svaret avhenger av hvem subjektet ER. Arbeidsrekkefølge: (1) Profiler subjektet " +
		"fra nærmeste kilde — det brukeren har fortalt, og aktørens egne sider når de finnes. (2) Si i " +
		"arbeidsnotatet hvilket kriterium relasjonen krever her, utledet av det brukeren skal beslutte. " +
		"(3) Let etter kandidater INNENFOR det segmentet, og les kandidatenes egne sider (fetch_url) før " +
		"du feller dom om portefølje eller posisjon — søkeutdrag og topplister er ikke evidens. " +
		"SVARKONTRAKT, alle tre delene: (a) kandidatene i løpende tekst med hver sin korte begrunnelse " +
		"mot kriteriet; (b) én setning som avgrenser mot aktørene som IKKE kvalifiserer — navngi kun " +
		"aktører som står i kildene eller samtalen, ellers beskriv kategorien uten navn; (c) hvis profilen har et konkret hull som begrenser svaret: avslutt med ETT spørsmål " +
		"om akkurat det hullet — dette spørsmålet er en del av leveransen, ikke vegring. " +
		"Tall og fakta om kandidater KUN ordrett fra kildene.",
	"anbefaling": " METODE: Etabler behovet fra samtalen før du leter. Søk etter kategorien og segmentet, " +
		"aldri «beste X»-fraser — topplister er ikke evidens. Les den aktuelle leverandørens EGEN side " +
		"(fetch_url) før du anbefaler. Lever ÉN anbefaling: hvorfor den passer akkurat denne " +
		"situasjonen, med pris og innhold KUN ordrett fra kilden — har du ikke lest prisen, ikke oppgi " +
		"den; si hvor den finnes. Avslutt med den ENE beslutningsregelen som ville snudd valget: bygger " +
		"den på noe kildene sier, si det; ellers merk den som din vurdering. Aldri en meny, aldri " +
		"diktede terskler. Ber brukeren eksplisitt om svar uten research: svar kort, men si at det er " +
		"fra hukommelsen og uverifisert, og tilby å sjekke — presenter aldri uverifisert som fakta.",
	"samtale": " METODE: Ingen verktøy trengs. Svar kort med husets stemme. Er det et reelt behov bak " +
		"småpraten, pek på det i én setning.",
}

// classMethod: promptsettets klasse → metodenøkkel ved -method auto.
var classMethod = map[string]string{
	"relasjon": "relasjon", "anbefaling": "anbefaling",
	"oppslag": "oppslag", "samtale": "samtale",
}

var webSearchTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "web_search",
		"description": "Søk på nettet. Bruk når svaret krever fersk informasjon eller fakta om " +
			"spesifikke entiteter (personer, selskaper, produkter, steder, hendelser) du ikke er " +
			"sikker på. Gjør ett kall per entitet/tema.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Selvstendig søkestreng"},
			},
			"required": []string{"query"},
		},
	},
}

var fetchURLTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "fetch_url",
		"description": "Hent og les hele innholdet fra ÉN konkret URL (nettside eller PDF). Bruk etter et " +
			"web_search når du trenger de faktiske tallene/detaljene fra en kilde, ikke bare snutten. " +
			"Oppgi en fullstendig url (inkl. https://).",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string"},
			},
			"required": []string{"url"},
		},
	},
}

type probeCase struct {
	ID    string `json:"id"`
	Class string `json:"class"`
	Text  string `json:"text"`
}

type roundLog struct {
	Thought   string          `json:"thought,omitempty"` // prosa levert SAMMEN med verktøykall
	ToolCalls []toolCallLog   `json:"tool_calls,omitempty"`
	Usage     map[string]int  `json:"usage,omitempty"`
	LatencyMS int64           `json:"latency_ms"`
	Raw       json.RawMessage `json:"-"`
}

type toolCallLog struct {
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

type transcript struct {
	Case       probeCase  `json:"case"`
	Variant    string     `json:"variant"`
	Rounds     []roundLog `json:"rounds"`
	Answer     string     `json:"answer"`
	Searches   int        `json:"searches"`
	Fetches    int        `json:"fetches"`
	PromptTok  int        `json:"prompt_tokens"`
	ComplTok   int        `json:"completion_tokens"`
	TotalMS    int64      `json:"total_ms"`
	Err        string     `json:"err,omitempty"`
	ReasonNote string     `json:"reason_note,omitempty"` // 422-fallback o.l.
}

func main() {
	set := flag.String("set", "cmd/v6probe/testdata/dev.jsonl", "promptsett")
	think := flag.Bool("think", false, "tenk-så-handle-instruks (P1)")
	method := flag.String("method", "", "\"\"=naken, auto=klassens blokk, ellers nøkkel")
	reason := flag.String("reason", "", "\"\"=utelat, on/off (P2)")
	only := flag.String("only", "", "kjør bare én id")
	rounds := flag.Int("rounds", 6, "maks verktøyrunder")
	outDir := flag.String("out", "probe_runs", "utkatalog")
	model := flag.String("model", "mistral-large-2512", "modell")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	sc := search.NewClient(cfg.SearxURL, cfg.SerperKey)
	client := &http.Client{Timeout: 180 * time.Second}

	cases, err := loadSet(*set)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	variant := fmt.Sprintf("think=%v method=%s reason=%s model=%s", *think, *method, *reason, *model)
	dir := filepath.Join(*outDir, time.Now().Format("20060102-150405"))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "meta.txt"), []byte(variant+"\nset="+*set+"\n"), 0o644)

	for _, c := range cases {
		if *only != "" && c.ID != *only {
			continue
		}
		t := runCase(client, sc, cfg, c, *think, *method, *reason, *model, *rounds)
		t.Variant = variant
		raw, _ := json.MarshalIndent(t, "", "  ")
		os.WriteFile(filepath.Join(dir, c.ID+".json"), raw, 0o644)
		thought := ""
		for _, r := range t.Rounds {
			if r.Thought != "" {
				thought = "TANKE✓"
				break
			}
		}
		fmt.Printf("%-4s %-10s runder=%d søk=%d hent=%d tok=%d/%d %6dms %s  %.80q\n",
			c.ID, c.Class, len(t.Rounds), t.Searches, t.Fetches,
			t.PromptTok, t.ComplTok, t.TotalMS, thought, oneLine(t.Answer))
	}
	fmt.Println("\nTranskript:", dir)
}

func loadSet(path string) ([]probeCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []probeCase
	scn := bufio.NewScanner(f)
	scn.Buffer(make([]byte, 1<<20), 1<<20)
	for scn.Scan() {
		line := strings.TrimSpace(scn.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c probeCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s: %w", line[:min(40, len(line))], err)
		}
		out = append(out, c)
	}
	return out, scn.Err()
}

func runCase(client *http.Client, sc *search.Client, cfg config.Config, c probeCase,
	think bool, method, reason, model string, maxRounds int) transcript {

	t := transcript{Case: c}
	system := "I dag er " + time.Now().Format("2006-01-02") + ". " + baseVoice + baseSearch + baseExecute
	if think {
		system += thinkRule
	}
	mkey := method
	if method == "auto" {
		mkey = classMethod[c.Class]
	}
	if m, ok := methods[mkey]; ok {
		system += m
	}

	msgs := []any{
		map[string]any{"role": "system", "content": system},
		map[string]any{"role": "user", "content": c.Text},
	}
	start := time.Now()
	reasonField := reason // kan nullstilles ved 422

	for round := 0; round < maxRounds; round++ {
		payload := map[string]any{
			"model": model, "messages": msgs, "stream": false, "temperature": 0.3,
			"tools": []any{webSearchTool, fetchURLTool},
		}
		if reasonField == "on" {
			payload["reasoning"] = map[string]any{"enabled": true}
		} else if reasonField == "off" {
			payload["reasoning"] = map[string]any{"enabled": false}
		}
		rStart := time.Now()
		status, body, err := post(client, cfg, payload)
		if err != nil {
			t.Err = err.Error()
			break
		}
		if status == 422 && reasonField != "" {
			// Leverandøren avviser reasoning-feltet: noter og prøv uten (P2-funn).
			t.ReasonNote = "422 med reasoning-felt; fortsetter uten"
			reasonField = ""
			round--
			continue
		}
		if status != 200 {
			t.Err = fmt.Sprintf("HTTP %d: %s", status, oneLine(string(body[:min(300, len(body))])))
			break
		}
		var out struct {
			Choices []struct {
				Message struct {
					Content   any `json:"content"`
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
		if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
			t.Err = "uparsbart svar: " + oneLine(string(body[:min(200, len(body))]))
			break
		}
		t.PromptTok += out.Usage.PromptTokens
		t.ComplTok += out.Usage.CompletionTokens
		msg := out.Choices[0].Message
		content := contentText(msg.Content)
		rl := roundLog{LatencyMS: time.Since(rStart).Milliseconds()}

		if len(msg.ToolCalls) == 0 {
			// Ferdig: sluttsvar.
			t.Answer = strings.TrimSpace(content)
			t.Rounds = append(t.Rounds, rl)
			break
		}
		rl.Thought = strings.TrimSpace(content)

		assistantCalls := make([]any, 0, len(msg.ToolCalls))
		toolMsgs := make([]any, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			var args struct {
				Query string `json:"query"`
				URL   string `json:"url"`
			}
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
			var result string
			switch tc.Function.Name {
			case "web_search":
				t.Searches++
				result = runSearch(sc, args.Query)
			case "fetch_url":
				t.Fetches++
				result = runFetch(sc, args.URL)
			default:
				result = "ukjent verktøy"
			}
			rl.ToolCalls = append(rl.ToolCalls, toolCallLog{Name: tc.Function.Name, Args: tc.Function.Arguments, Result: result})
			assistantCalls = append(assistantCalls, map[string]any{
				"id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Function.Name, "arguments": tc.Function.Arguments},
			})
			toolMsgs = append(toolMsgs, map[string]any{
				"role": "tool", "tool_call_id": tc.ID, "name": tc.Function.Name, "content": result,
			})
		}
		t.Rounds = append(t.Rounds, rl)
		msgs = append(msgs, map[string]any{"role": "assistant", "content": content, "tool_calls": assistantCalls})
		msgs = append(msgs, toolMsgs...)
	}
	t.TotalMS = time.Since(start).Milliseconds()
	return t
}

func post(client *http.Client, cfg config.Config, payload map[string]any) (int, []byte, error) {
	// Leverandørvalg som i prod-eksperimentet: Large 3 bor hos Mistral La
	// Plateforme (MISTRAL_API_KEY fra env via config.Load sin .env-lasting),
	// alt annet hos Scaleway. Mistral svarer 422 på «reasoning»-feltet — det
	// fjernes der (målt i prod: kallet døde på 80 ms).
	base, key := strings.TrimSuffix(cfg.UpstreamBaseURL, "/"), cfg.UpstreamAPIKey
	if m, _ := payload["model"].(string); strings.HasPrefix(m, "mistral-large") {
		if mk := os.Getenv("MISTRAL_API_KEY"); mk != "" {
			base, key = "https://api.mistral.ai/v1", mk
			delete(payload, "reasoning")
		}
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		base+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes(), nil
}

// runSearch: ekte søk + FULLE sidehentinger, formatert som kompakt kontekst.
// Samme filosofi som prod (hele sider, aldri bare snutter); rangeringen er
// forenklet (trunkering) siden proben ikke drar inn embedding-laget.
func runSearch(sc *search.Client, query string) string {
	if strings.TrimSpace(query) == "" {
		return "Tomt søk."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	results, err := sc.Search(ctx, query)
	if err != nil || len(results) == 0 {
		wiki := sc.WikiSearch(ctx, query)
		if len(wiki) == 0 {
			return "Søket ga ingen resultater."
		}
		results = wiki
	}
	if len(results) > 4 {
		results = results[:4]
	}
	pages := sc.FetchPages(ctx, results, 6000)
	var b strings.Builder
	fmt.Fprintf(&b, "Søkeresultater for «%s»:\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "\n### Kilde %d: %s — %s\n%s\n", i+1, r.Title, r.URL, r.Description)
		if i < len(pages) && strings.TrimSpace(pages[i]) != "" {
			fmt.Fprintln(&b, firstRunes(pages[i], 2500))
		}
	}
	b.WriteString("\nTrenger du mer enn dette: kall fetch_url på den mest relevante kilden.")
	return b.String()
}

func runFetch(sc *search.Client, url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return "Mangler url."
	}
	if !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	text := sc.FetchURL(ctx, url, 6000)
	if strings.TrimSpace(text) == "" {
		return "Fant ikke lesbart innhold på siden."
	}
	return text
}

func contentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
