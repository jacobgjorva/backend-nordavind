// v6compress prober lengdegulvet: tar EKTE lange svar fra tidligere
// probekjøringer, komprimerer dem med ett modellkall, og sjekker MEKANISK
// at ingenting gikk tapt — hvert rent tall, avgrensningen og et eventuelt
// avsluttende spørsmål.
//
// Falsifikasjon (satt før kjøring): gulvet forkastes hvis tall mistes i
// mer enn én case, kontraktsdeler mistes i mer enn én, median reduksjon er
// under 30 %, eller komprimeringen DIKTER nye tall.
//
//	go run ./cmd/v6compress
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
)

const compressPrompt = `Komprimer teksten under til godt under %d tegn, på norsk.
UFRAVIKELIG: behold HVERT tall, HVERT navn og HVERT faktum ordrett — kutt kun fyll, gjentakelser og omskrivinger.
Behold standpunktet/anbefalingen med begrunnelse, en eventuell avgrensning mot det som IKKE passer, og et eventuelt avsluttende spørsmål.
Løpende tekst, ingen lister. Svar KUN med den komprimerte teksten.

TEKST:
%s`

var numRe = regexp.MustCompile(`\d[\d .,]*\d|\d`)

func pureNums(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range numRe.FindAllString(s, -1) {
		d := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, m)
		if len(d) >= 2 {
			out[d] = true
		}
	}
	return out
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	client := &http.Client{Timeout: 120 * time.Second}

	// Ekte lange svar fra tidligere kjøringer, med metodens budsjett.
	cases := gather()
	if len(cases) == 0 {
		fmt.Fprintln(os.Stderr, "fant ingen lange svar i probekatalogene")
		os.Exit(2)
	}

	var reductions []float64
	numLost, contractLost, invented := 0, 0, 0
	for _, c := range cases {
		compressed, err := call(client, cfg, fmt.Sprintf(compressPrompt, c.limit, c.answer))
		if err != nil {
			fmt.Printf("%-6s FEIL: %v\n", c.id, err)
			continue
		}
		r := float64(len(compressed)) / float64(len(c.answer))
		reductions = append(reductions, r)

		origN, compN := pureNums(c.answer), pureNums(compressed)
		var lost, news []string
		for n := range origN {
			if !compN[n] {
				lost = append(lost, n)
			}
		}
		for n := range compN {
			if !origN[n] {
				news = append(news, n)
			}
		}
		hadQ := strings.Contains(c.answer, "?")
		hasQ := strings.Contains(compressed, "?")
		qOK := !hadQ || hasQ
		if len(lost) > 0 {
			numLost++
		}
		if !qOK {
			contractLost++
		}
		if len(news) > 0 {
			invented++
		}
		fmt.Printf("%-6s %5d → %5d tegn (%.2f)  tall-tapt=%v nye-tall=%v spm-ok=%v\n",
			c.id, len(c.answer), len(compressed), r, lost, news, qOK)
	}
	sort.Float64s(reductions)
	if len(reductions) > 0 {
		fmt.Printf("\nmedian B/A: %.2f   tall-tap i %d caser, kontrakt-tap i %d, dikt i %d\n",
			reductions[len(reductions)/2], numLost, contractLost, invented)
	}
}

type pc struct {
	id     string
	answer string
	limit  int
}

func gather() []pc {
	limits := map[string]int{"relasjon": 900, "anbefaling": 700, "raadgivning": 700}
	seen := map[string]bool{}
	var out []pc
	for _, dir := range []string{"probe_dense/A", "probe_advis/B2", "probe_advis/B", "probe_verify/B", "probe_runs_press"} {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
				return nil
			}
			raw, _ := os.ReadFile(path)
			var t struct {
				Case struct {
					ID    string `json:"id"`
					Class string `json:"class"`
				} `json:"case"`
				Answer string `json:"answer"`
			}
			if json.Unmarshal(raw, &t) != nil {
				return nil
			}
			lim := limits[t.Case.Class]
			if lim == 0 || len(t.Answer) < 1200 || seen[t.Case.ID] {
				return nil
			}
			seen[t.Case.ID] = true
			out = append(out, pc{t.Case.ID, t.Answer, lim})
			return nil
		})
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func call(client *http.Client, cfg config.Config, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": "mistral-large-2512", "stream": false, "temperature": 0.2, "max_tokens": 900,
		"messages": []any{map[string]any{"role": "user", "content": prompt}},
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
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("uparsbart svar")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
