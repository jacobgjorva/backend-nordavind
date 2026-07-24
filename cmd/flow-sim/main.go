// flow-sim er produktsimulatoren: kjører prompt-suiten mot en KJØRENDE backend
// som en ekte klient, måler tid, lar en uavhengig dommer-agent score hvert
// svar mot våre kriterier, logger manglende evner (verktøy vi ikke har), og
// lar en bygge-agent foreslå komplette flyt-rader for hyller som scorer svakt.
//
// Forslag auto-appliseres IKKE — de skrives som rapport (sim_runs/<ts>/) og
// gjennomgås før registeret/flyt-tabellen endres. Intent-eval er fortsatt
// porten for enhver endring.
//
//	NORDAVIND_TOKEN=<sesjonstoken> go run ./cmd/flow-sim
//	  -base http://localhost:8080   backend som testes
//	  -judge-only <run-dir>         re-scor en tidligere kjøring
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
	"sort"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

type simCase struct {
	Shelf string `json:"shelf"`
	Text  string `json:"text"`
}

type simResult struct {
	Case      simCase `json:"case"`
	Answer    string  `json:"answer"`
	Steps     string  `json:"steps"`
	TTFTMS    int64   `json:"ttft_ms"`
	TotalMS   int64   `json:"total_ms"`
	Err       string  `json:"err,omitempty"`
	// Dommer-felter (fylles i score-fasen).
	Brevity     int    `json:"brevity"`
	Correctness int    `json:"correctness"`
	Value       int    `json:"value"`
	MissingTool string `json:"missing_tool,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// judgeRubric er kriteriene VÅRE — dommeren scorer aldri etter egne ideer.
const judgeRubric = `Du er en streng produktdommer for en norsk bedrifts-AI. Vurder SVARET på BRUKERENS melding etter disse kriteriene:
- brevity (1-5): 5 = kortest mulig uten tap av verdi (ett tall/én setning når det holder); 1 = pratsomt, gjentakelser, fyll.
- correctness (1-5): 5 = svarer presist på det som ble spurt, riktig form (tabell når bedt om, tall når bedt om); 1 = svarer på noe annet, gjetter, eller nekter uten grunn.
- value (1-5): 5 = brukeren er ferdig hjulpet; 1 = brukeren må gjøre jobben selv.
- missing_tool: hvis svaret viser at assistenten MANGLER en evne (kan ikke sende e-post, ikke lese innboks, ikke søke filer e.l.), navngi evnen kort på engelsk (f.eks. "send_email"), ellers tom streng.
Svar KUN med JSON: {"brevity":n,"correctness":n,"value":n,"missing_tool":"...","comment":"én kort setning"}`

const builderSystem = `Du designer flyt-rader for en intent-motor. En flyt-rad har feltene:
tools (liste fra verktøykatalogen UNDER — aldri dikt opp nye), model ("mid"|"heavy"|"top"), max_chars (mykt svartak), deterministic (bool), fallback ("free_chat"), sticky (bool).
Du får en hylle med svake scorer + eksempelsvar. Foreslå: flow (feltene over), registry_examples (5-8 norske eksempel-ytringer), eval_lines (3 nye testlinjer {text,want}), og missing_tools (evner som MÅ bygges før flyten kan virke — kun hvis verktøykatalogen ikke dekker behovet).
Svar KUN med JSON: {"flow":{...},"registry_examples":[...],"eval_lines":[...],"missing_tools":[...],"reasoning":"2-3 setninger"}`

func main() {
	base := flag.String("base", "http://localhost:8080", "backend-URL")
	judgeOnly := flag.String("judge-only", "", "re-scor en tidligere kjøring (run-dir)")
	flag.Parse()

	token := os.Getenv("NORDAVIND_TOKEN")
	if token == "" && *judgeOnly == "" {
		fmt.Fprintln(os.Stderr, "sett NORDAVIND_TOKEN (sesjonstoken)")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	client := &http.Client{Timeout: 120 * time.Second}

	var results []simResult
	runDir := *judgeOnly
	if runDir == "" {
		runDir = filepath.Join("sim_runs", time.Now().Format("20060102-150405"))
		os.MkdirAll(runDir, 0o755)
		results = runSuite(client, *base, token)
		save(runDir, "results.json", results)
	} else {
		raw, err := os.ReadFile(filepath.Join(runDir, "results.json"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		json.Unmarshal(raw, &results)
	}

	// Fase 2: dommeren scorer hvert svar.
	for i := range results {
		if results[i].Err != "" {
			continue
		}
		scoreOne(client, cfg, &results[i])
		fmt.Printf("%-20s brev=%d korr=%d verdi=%d %4dms  %s\n",
			results[i].Case.Shelf, results[i].Brevity, results[i].Correctness,
			results[i].Value, results[i].TotalMS, results[i].MissingTool)
	}
	save(runDir, "results.json", results)

	// Fase 3: oppsummering + manglende verktøy + bygge-forslag for svake hyller.
	summarize(runDir, client, cfg, results)
}

func runSuite(client *http.Client, base, token string) []simResult {
	f, err := os.Open("internal/intent/testdata/sim_prompts.jsonl")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer f.Close()
	var out []simResult
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var c simCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			continue
		}
		r := simResult{Case: c}
		r.Answer, r.Steps, r.TTFTMS, r.TotalMS, r.Err = askBackend(client, base, token, c.Text)
		fmt.Printf("kjørt: %-20s %4dms  %.60q\n", c.Shelf, r.TotalMS, r.Answer)
		out = append(out, r)
	}
	return out
}

// askBackend sender én melding som en ekte klient (streaming) og samler svaret.
func askBackend(client *http.Client, base, token, text string) (answer, steps string, ttftMS, totalMS int64, errStr string) {
	body, _ := json.Marshal(map[string]any{
		"model": "auto", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": text}},
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, 0, err.Error()
	}
	defer resp.Body.Close()
	var b strings.Builder
	var stepList []string
	var ttft time.Duration
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var d struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Step string `json:"nordavind_step"`
		}
		if json.Unmarshal([]byte(line[6:]), &d) != nil {
			continue
		}
		if d.Step != "" {
			stepList = append(stepList, d.Step)
		}
		if len(d.Choices) > 0 && d.Choices[0].Delta.Content != "" {
			if ttft == 0 {
				ttft = time.Since(start)
			}
			b.WriteString(d.Choices[0].Delta.Content)
		}
	}
	return b.String(), strings.Join(stepList, " → "), ttft.Milliseconds(), time.Since(start).Milliseconds(), ""
}

func scoreOne(client *http.Client, cfg config.Config, r *simResult) {
	user := fmt.Sprintf("BRUKERENS MELDING:\n%s\n\nSVARET:\n%s\n\n(prosess-steg: %s)", r.Case.Text, r.Answer, r.Steps)
	raw, err := llmJSON(client, cfg, router.TopModel, judgeRubric, user, 300)
	if err != nil {
		r.Comment = "dommer feilet: " + err.Error()
		return
	}
	var v struct {
		Brevity     int    `json:"brevity"`
		Correctness int    `json:"correctness"`
		Value       int    `json:"value"`
		MissingTool string `json:"missing_tool"`
		Comment     string `json:"comment"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		r.Comment = "uparsbar dom: " + truncate(raw, 120)
		return
	}
	r.Brevity, r.Correctness, r.Value = v.Brevity, v.Correctness, v.Value
	r.MissingTool, r.Comment = v.MissingTool, v.Comment
}

func summarize(runDir string, client *http.Client, cfg config.Config, results []simResult) {
	// Manglende verktøy på tvers av suiten — dette er utviklings-backloggen.
	missing := map[string][]string{}
	weak := map[string][]simResult{}
	for _, r := range results {
		if r.MissingTool != "" {
			missing[r.MissingTool] = append(missing[r.MissingTool], r.Case.Shelf)
		}
		if r.Err == "" && (r.Value <= 3 || r.Correctness <= 3) {
			weak[r.Case.Shelf] = append(weak[r.Case.Shelf], r)
		}
	}
	fmt.Println("\n== Manglende verktøy (utviklings-backlog) ==")
	var names []string
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-18s trengs av: %s\n", n, strings.Join(missing[n], ", "))
	}

	// Bygge-agenten foreslår flyt-rader for svake hyller.
	toolCatalog := strings.Join([]string{
		intent.ToolWebSearch, intent.ToolFetchURL, intent.ToolQueryDatabase,
		intent.ToolShowTable, intent.ToolM365Search, intent.ToolM365Read,
		intent.ToolContactPerson, intent.ToolSetWidget, intent.ToolConnectDB,
		intent.ToolConnectM365, intent.ToolSetupRoutine, intent.ToolUpdateAgent,
		intent.ToolListAgents,
	}, ", ")
	proposals := map[string]json.RawMessage{}
	for shelf, rs := range weak {
		var sb strings.Builder
		fmt.Fprintf(&sb, "HYLLE: %s\nVERKTØYKATALOG: %s\n\nSVAKE SVAR:\n", shelf, toolCatalog)
		for _, r := range rs {
			fmt.Fprintf(&sb, "- «%s» → «%s» (brev=%d korr=%d verdi=%d, mangler=%s)\n",
				r.Case.Text, truncate(r.Answer, 200), r.Brevity, r.Correctness, r.Value, r.MissingTool)
		}
		raw, err := llmJSON(client, cfg, router.TopModel, builderSystem, sb.String(), 1200)
		if err != nil {
			fmt.Printf("bygge-agent feilet for %s: %v\n", shelf, err)
			continue
		}
		proposals[shelf] = json.RawMessage(raw)
		fmt.Printf("\n== Forslag for %s ==\n%s\n", shelf, raw)
	}
	save(runDir, "proposals.json", proposals)
	fmt.Printf("\nAlt lagret i %s — gjennomgå proposals.json før noe endres.\n", runDir)
}

func llmJSON(client *http.Client, cfg config.Config, model, system, user string, maxTokens int) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": user},
		},
		"stream": false, "temperature": 0.1, "max_tokens": maxTokens,
		"reasoning": map[string]any{"enabled": false},
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		strings.TrimSuffix(cfg.UpstreamBaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.UpstreamAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("tomt svar")
	}
	c := strings.TrimSpace(out.Choices[0].Message.Content)
	c = strings.TrimPrefix(c, "```json")
	c = strings.Trim(c, "` \n")
	return c, nil
}

func save(dir, name string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(filepath.Join(dir, name), b, 0o644)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
