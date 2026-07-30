// v6ab kjører holdt-tilbake-settet mot en KJØRENDE backend som en ekte
// klient, og lagrer svarene for blindlesing.
//
// Begge sider kjøres mot samme binær, samme sett og samme dag — kun
// ENGINE-variabelen skiller dem. Kjør A og B etter hverandre og bland
// resultatene med -pair.
//
//	# legacy
//	ENGINE= AUTH_REQUIRED=false INTENT_ENGINE=on go run ./cmd/server &
//	go run ./cmd/v6ab -side A -out ab_runs
//
//	# v6
//	ENGINE=v6 AUTH_REQUIRED=false INTENT_ENGINE=on go run ./cmd/server &
//	go run ./cmd/v6ab -side B -out ab_runs
//
//	# anonymisert lesefil
//	go run ./cmd/v6ab -pair ab_runs
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type abCase struct {
	ID    string `json:"id"`
	Class string `json:"class"`
	Text  string `json:"text"`
}

type abResult struct {
	Case    abCase   `json:"case"`
	Side    string   `json:"side"`
	Answer  string   `json:"answer"`
	Steps   []string `json:"steps"`
	Sources int      `json:"sources"`
	TTFTMS  int64    `json:"ttft_ms"`
	TotalMS int64    `json:"total_ms"`
	Err     string   `json:"err,omitempty"`
}

func main() {
	base := flag.String("base", "http://localhost:8080", "backend-URL")
	set := flag.String("set", "cmd/v6ab/testdata/holdout.jsonl", "promptsett")
	side := flag.String("side", "", "A (legacy) eller B (v6)")
	out := flag.String("out", "ab_runs", "utkatalog")
	pair := flag.String("pair", "", "bygg anonymisert lesefil fra en katalog")
	flag.Parse()

	if *pair != "" {
		if err := buildBlindFile(*pair); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if *side != "A" && *side != "B" {
		fmt.Fprintln(os.Stderr, "sett -side A eller -side B")
		os.Exit(2)
	}

	cases, err := load(*set)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	client := &http.Client{Timeout: 300 * time.Second}
	token := os.Getenv("NORDAVIND_TOKEN") // tom er greit med AUTH_REQUIRED=false

	dir := filepath.Join(*out, *side)
	os.MkdirAll(dir, 0o755)

	for _, c := range cases {
		r := run(client, *base, token, c, *side)
		raw, _ := json.MarshalIndent(r, "", "  ")
		os.WriteFile(filepath.Join(dir, c.ID+".json"), raw, 0o644)
		status := "ok"
		if r.Err != "" {
			status = "FEIL: " + r.Err
		}
		fmt.Printf("%-4s %-10s steg=%-2d kilder=%-2d %6dms  %s %s\n",
			c.ID, c.Class, len(r.Steps), r.Sources, r.TotalMS, status, brief(r.Answer))
	}
	fmt.Println("\nLagret i", dir)
}

func run(client *http.Client, base, token string, c abCase, side string) abResult {
	r := abResult{Case: c, Side: side}
	body, _ := json.Marshal(map[string]any{
		"model": "auto", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": c.Text}},
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer resp.Body.Close()

	var answer strings.Builder
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
			Step    string `json:"nordavind_step"`
			Sources []struct {
				URL string `json:"url"`
			} `json:"nordavind_sources"`
		}
		if json.Unmarshal([]byte(line[6:]), &d) != nil {
			continue
		}
		if d.Step != "" {
			r.Steps = append(r.Steps, d.Step)
		}
		r.Sources += len(d.Sources)
		if len(d.Choices) > 0 && d.Choices[0].Delta.Content != "" {
			if ttft == 0 {
				ttft = time.Since(start)
			}
			answer.WriteString(d.Choices[0].Delta.Content)
		}
	}
	r.Answer = strings.TrimSpace(answer.String())
	r.TTFTMS, r.TotalMS = ttft.Milliseconds(), time.Since(start).Milliseconds()
	return r
}

// buildBlindFile lager lesefila: for hver prompt vises begge svar som
// «Variant 1» og «Variant 2», med siden skjult. Rekkefølgen varierer
// deterministisk per case, så leseren ikke kan lære et mønster.
//
// Fasit skrives til en EGEN fil som ikke åpnes før lesingen er ferdig.
func buildBlindFile(dir string) error {
	type pairT struct{ a, b abResult }
	pairs := map[string]*pairT{}

	for _, side := range []string{"A", "B"} {
		files, _ := filepath.Glob(filepath.Join(dir, side, "*.json"))
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var r abResult
			if json.Unmarshal(raw, &r) != nil {
				continue
			}
			p := pairs[r.Case.ID]
			if p == nil {
				p = &pairT{}
				pairs[r.Case.ID] = p
			}
			if side == "A" {
				p.a = r
			} else {
				p.b = r
			}
		}
	}
	if len(pairs) == 0 {
		return fmt.Errorf("fant ingen resultater i %s", dir)
	}

	ids := make([]string, 0, len(pairs))
	for id := range pairs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var doc, key strings.Builder
	doc.WriteString("# Blindlesing — holdt-tilbake-settet\n\n")
	doc.WriteString("For hver oppgave: hvilken variant gir MEST verdi for brukeren?\n")
	doc.WriteString("Se etter: forstår den hva som egentlig spørres om, avgrenser den\n")
	doc.WriteString("mot det som ikke kvalifiserer, er tallene til å stole på, og er\n")
	doc.WriteString("svaret til å handle på.\n\n")
	key.WriteString("# FASIT — åpnes først NÅR lesingen er ferdig\n\n")

	for i, id := range ids {
		p := pairs[id]
		// Deterministisk, men vekslende rekkefølge.
		first, second := p.a, p.b
		firstSide, secondSide := "A (legacy)", "B (v6)"
		if i%2 == 1 {
			first, second = p.b, p.a
			firstSide, secondSide = "B (v6)", "A (legacy)"
		}
		fmt.Fprintf(&doc, "---\n\n## %s\n\n**Oppgave:** %s\n\n", id, p.a.Case.Text)
		fmt.Fprintf(&doc, "### Variant 1\n\n%s\n\n", orDash(first.Answer))
		fmt.Fprintf(&doc, "### Variant 2\n\n%s\n\n", orDash(second.Answer))
		fmt.Fprintf(&key, "%s: Variant 1 = %s, Variant 2 = %s  (steg %d/%d, %d/%d ms)\n",
			id, firstSide, secondSide, len(first.Steps), len(second.Steps), first.TotalMS, second.TotalMS)
	}

	docPath := filepath.Join(dir, "BLINDLESING.md")
	keyPath := filepath.Join(dir, "FASIT.md")
	if err := os.WriteFile(docPath, []byte(doc.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(key.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("%d oppgaver → %s (fasit: %s)\n", len(ids), docPath, keyPath)
	return nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "_(tomt svar)_"
	}
	return s
}

func load(path string) ([]abCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []abCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c abCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

func brief(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60]) + "…"
	}
	return s
}
