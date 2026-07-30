// v6anchor måler om et relevansanker («spørsmål + modellens tanke») endrer
// hvilke kildeutdrag som overlever rangeringen.
//
// RESULTAT (docs/MOTOR-V6-PROBER.md, kjøring 8): 1 av 32 topputdrag byttet.
// Mekanismen ble derfor FJERNET fra motoren. Verktøyet står igjen som
// målemetoden — enhver senere idé om å endre rangeringsvektoren skal måles
// her før den bygges.
//
// Uten denne målingen vet vi bare at strengen settes sammen riktig — ikke
// at den gjør noen forskjell. Den kjører ekte søk og ekte embeddings, og
// rangerer de SAMME sidene to ganger: én gang mot spørsmålet alene (dagens
// oppførsel), én gang mot spørsmål + tanke (v6).
//
// KOSTNAD: ett søk og to embedding-kall per case. Ingen modellkall.
//
//	go run ./cmd/v6anchor
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
	"sort"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

// Case er ett spørsmål med den tanken modellen FAKTISK produserte på det
// spørsmålet (hentet ordrett fra loop_runs). Ingen oppdiktede tanker: da
// ville vi målt vår egen formuleringsevne, ikke mekanismen.
type anchorCase struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Thought  string `json:"thought"`
	Query    string `json:"query"`
}

const embeddingModel = "mistral-embed"

func main() {
	setPath := flag.String("set", "cmd/v6anchor/testdata/anchors.jsonl", "casene")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	sc := search.NewClient(cfg.SearxURL, cfg.SerperKey)
	client := &http.Client{Timeout: 120 * time.Second}

	cases, err := load(*setPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	var moved, total int
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		results, err := sc.Search(ctx, c.Query)
		if err != nil || len(results) == 0 {
			results = sc.WikiSearch(ctx, c.Query)
		}
		if len(results) > 4 {
			results = results[:4]
		}
		pages := sc.FetchPages(ctx, results, 6000)
		cancel()
		if len(pages) == 0 {
			fmt.Printf("%-4s ingen sider — hoppet over\n", c.ID)
			continue
		}

		chunks := splitAll(pages)
		if len(chunks) < 8 {
			fmt.Printf("%-4s for få avsnitt (%d) — hoppet over\n", c.ID, len(chunks))
			continue
		}

		embed := func(texts []string) ([][]float32, error) {
			return embedBatch(client, cfg, texts)
		}
		vecs, err := embed(chunks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: embedding feilet: %v\n", c.ID, err)
			continue
		}

		base, err := embed([]string{c.Question})
		if err != nil {
			continue
		}
		withThought, err := embed([]string{c.Question + "\n" + c.Thought})
		if err != nil {
			continue
		}

		topA := topN(vecs, chunks, base[0], 8)
		topB := topN(vecs, chunks, withThought[0], 8)

		same := overlap(topA, topB)
		changed := 8 - same
		moved += changed
		total += 8
		fmt.Printf("%-4s utdrag=%d  uendret=%d/8  byttet=%d  snitt-cos: spørsmål %.3f → +tanke %.3f\n",
			c.ID, len(chunks), same, changed, meanScore(vecs, chunks, base[0], topA), meanScore(vecs, chunks, withThought[0], topB))
		if changed > 0 {
			for _, t := range diff(topB, topA) {
				fmt.Printf("      NYTT: %s\n", brief(chunks[t]))
			}
		}
	}
	if total > 0 {
		fmt.Printf("\nTotalt byttet %d av %d topputdrag (%.0f %%)\n",
			moved, total, 100*float64(moved)/float64(total))
	}
}

func load(path string) ([]anchorCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []anchorCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c anchorCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// splitAll deler sidene i avsnitt på samme måte som utdragsmotoren:
// 200-700 tegn, ordrette delsekvenser.
func splitAll(pages []string) []string {
	var out []string
	for _, p := range pages {
		for _, line := range strings.Split(p, "\n") {
			line = strings.TrimSpace(line)
			for len([]rune(line)) > 700 {
				r := []rune(line)
				cut := 700
				if sp := strings.LastIndex(string(r[:700]), " "); sp > 200 {
					cut = len([]rune(string(r[:700])[:sp]))
				}
				out = append(out, strings.TrimSpace(string(r[:cut])))
				line = strings.TrimSpace(string(r[cut:]))
			}
			if len([]rune(line)) >= 200 {
				out = append(out, line)
			}
		}
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

type scored struct {
	idx   int
	score float64
}

func topN(vecs [][]float32, chunks []string, q []float32, n int) []int {
	all := make([]scored, 0, len(vecs))
	for i := range vecs {
		all = append(all, scored{i, cosine(q, vecs[i])})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > n {
		all = all[:n]
	}
	out := make([]int, 0, len(all))
	for _, s := range all {
		out = append(out, s.idx)
	}
	return out
}

func meanScore(vecs [][]float32, chunks []string, q []float32, idx []int) float64 {
	var sum float64
	for _, i := range idx {
		sum += cosine(q, vecs[i])
	}
	if len(idx) == 0 {
		return 0
	}
	return sum / float64(len(idx))
}

func overlap(a, b []int) int {
	set := map[int]bool{}
	for _, x := range a {
		set[x] = true
	}
	n := 0
	for _, x := range b {
		if set[x] {
			n++
		}
	}
	return n
}

func diff(b, a []int) []int {
	set := map[int]bool{}
	for _, x := range a {
		set[x] = true
	}
	var out []int
	for _, x := range b {
		if !set[x] {
			out = append(out, x)
		}
	}
	return out
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		if i >= len(b) {
			break
		}
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

func sqrt(f float64) float64 {
	if f <= 0 {
		return 0
	}
	x := f
	for i := 0; i < 24; i++ {
		x = 0.5 * (x + f/x)
	}
	return x
}

func embedBatch(client *http.Client, cfg config.Config, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": embeddingModel, "input": texts})
	url := strings.TrimSuffix(cfg.UpstreamBaseURL, "/") + "/embeddings"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.UpstreamAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	res := make([][]float32, len(out.Data))
	for _, d := range out.Data {
		if d.Index < len(res) {
			res[d.Index] = d.Embedding
		}
	}
	return res, nil
}

func brief(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 110 {
		s = string([]rune(s)[:110]) + "…"
	}
	return s
}
