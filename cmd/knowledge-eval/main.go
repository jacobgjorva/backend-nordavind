// knowledge-eval er porten for kunnskapshentingen (G6 i docs/KNOWLEDGE.md):
// seeder en fersk in-memory-tenant med fasit-fixtures, kjører hver eval-
// spørring gjennom NØYAKTIG samme hentekode som prod (knowledgeContext) og
// måler de tre målene: presisjon (want-markører i injisert kontekst),
// injiserte tegn og latens. Kun embeddings-kall — ingen LLM, øre-kostnad.
//
//	go run ./cmd/knowledge-eval            baseline / port-kjøring
//	go run ./cmd/knowledge-eval -min 0.9   rød under 90 % treff
//	go run ./cmd/knowledge-eval -verbose   vis hver spørring
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/api"
	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

type fixture struct {
	Kind     string   `json:"kind"` // fact | doc | node (uttrukket node med kant til doc)
	Title    string   `json:"title"`
	Text     string   `json:"text"`
	Filename string   `json:"filename"`
	Chunks   []string `json:"chunks"`
	Doc      string   `json:"doc"` // node: filnavnet til dokumentet noden er uttrukket fra
}

type evalCase struct {
	Query string   `json:"query"`
	Want  []string `json:"want"`
	Empty bool     `json:"empty"` // irrelevant spørring: konteksten SKAL være tom
}

const tenant = "eval-tenant"

var noNoise = flag.Bool("no-noise", false, "kjør uten støylappene (gammel baseline)")

func main() {
	verbose := flag.Bool("verbose", false, "vis hver spørring")
	min := flag.Float64("min", 0, "rød (exit 1) under denne treffraten, 0 = kun rapport")
	dbURL := flag.String("db", "", "kjør mot denne databasen (postgres://…) i stedet for temp-SQLite")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	// Fersk, isolert database i en temp-katalog — aldri prod-data i fasiten.
	dir, err := os.MkdirTemp("", "knowledge-eval-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)
	cfg.DBPath = filepath.Join(dir, "eval.db")
	if *dbURL != "" {
		cfg.DBPath = *dbURL
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := api.NewServer(cfg, log, st)

	embedder := &intent.ScalewayEmbedder{
		BaseURL: cfg.UpstreamBaseURL, APIKey: cfg.UpstreamAPIKey,
		Model: "qwen3-embedding-8b", Client: &http.Client{Timeout: 30 * time.Second},
	}
	ctx := context.Background()

	fixtures := readJSONL[fixture]("internal/api/testdata/knowledge-fixtures.jsonl")
	// Støy: uten den måler vi henting i en base på 26 lapper, mens prod har
	// hundrevis. Da fylles budsjettet av naboer, og tallene her ville sagt
	// «792 tegn» mens virkeligheten sender 4 000.
	if !*noNoise {
		fixtures = append(fixtures,
			readJSONL[fixture]("internal/api/testdata/knowledge-noise.jsonl")...)
	}
	cases := readJSONL[evalCase]("internal/api/testdata/knowledge-eval.jsonl")

	// Seed: embed og lagre nøyaktig slik prod-veiene gjør (fakta via
	// SyncFactNote, dokumenter via CreateDocumentNotes, uttrukne noder som
	// aksepterte lapper med kant til dok-noden — G2/G4-løypa).
	nFacts, nChunks, nNodes := 0, 0, 0
	docIDs := map[string]string{} // filnavn → dok-id
	for i, f := range fixtures {
		switch f.Kind {
		case "fact":
			vecs, err := embedder.Embed(ctx, []string{f.Title + ". " + f.Text})
			if err != nil {
				fmt.Fprintln(os.Stderr, "embed:", err)
				os.Exit(2)
			}
			id := fmt.Sprintf("eval-fact-%d", i)
			if err := st.SyncFactNote(tenant, id, f.Title, f.Text, vecs[0]); err != nil {
				fmt.Fprintln(os.Stderr, "seed:", err)
				os.Exit(2)
			}
			nFacts++
		case "doc":
			vecs, err := embedder.Embed(ctx, f.Chunks)
			if err != nil {
				fmt.Fprintln(os.Stderr, "embed:", err)
				os.Exit(2)
			}
			notes := make([]store.KnowledgeNote, len(f.Chunks))
			for j, c := range f.Chunks {
				notes[j] = store.KnowledgeNote{Text: c, Embedding: vecs[j]}
			}
			doc := store.DocumentInput{Filename: f.Filename, RawText: strings.Join(f.Chunks, "\n"), Title: f.Title}
			docID, err := st.CreateDocumentNotes(tenant, doc, notes)
			if err != nil {
				fmt.Fprintln(os.Stderr, "seed doc:", err)
				os.Exit(2)
			}
			docIDs[f.Filename] = docID
			nChunks += len(f.Chunks)
		case "node":
			vecs, err := embedder.Embed(ctx, []string{f.Title + ". " + f.Text})
			if err != nil {
				fmt.Fprintln(os.Stderr, "embed:", err)
				os.Exit(2)
			}
			id := fmt.Sprintf("eval-node-%d", i)
			if err := st.SyncFactNote(tenant, id, f.Title, f.Text, vecs[0]); err != nil {
				fmt.Fprintln(os.Stderr, "seed node:", err)
				os.Exit(2)
			}
			docID, ok := docIDs[f.Doc]
			if !ok {
				fmt.Fprintln(os.Stderr, "node-fixture peker på ukjent doc:", f.Doc)
				os.Exit(2)
			}
			if err := st.AddEdge(tenant, store.KnowledgeEdge{FromID: id, ToID: docID, Relation: "definert i"}); err != nil {
				fmt.Fprintln(os.Stderr, "seed edge:", err)
				os.Exit(2)
			}
			nNodes++
		}
	}
	fmt.Printf("seedet %d fakta + %d dokument-biter + %d uttrukne noder\n\n", nFacts, nChunks, nNodes)

	hits, chars := 0, 0
	var times []time.Duration
	for _, c := range cases {
		t0 := time.Now()
		got := srv.EvalKnowledge(ctx, tenant, c.Query)
		dur := time.Since(t0)
		times = append(times, dur)
		chars += len(got)

		ok := true
		if c.Empty {
			ok = strings.TrimSpace(got) == ""
		} else {
			for _, w := range c.Want {
				if !strings.Contains(strings.ToLower(got), strings.ToLower(w)) {
					ok = false
				}
			}
		}
		if ok {
			hits++
		}
		if *verbose || !ok {
			mark := "TREFF"
			if !ok {
				mark = "BOM  "
			}
			fmt.Printf("%s %4dms %4d tegn  %s\n", mark, dur.Milliseconds(), len(got), c.Query)
			if !ok && !c.Empty {
				fmt.Printf("      mangler: %v\n", c.Want)
			}
		}
	}

	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	rate := float64(hits) / float64(len(cases))
	fmt.Printf("\ntreff: %d/%d (%.0f %%)  snitt-tegn: %d  p50: %dms  p95: %dms\n",
		hits, len(cases), rate*100, chars/len(cases),
		times[len(times)/2].Milliseconds(), times[len(times)*95/100].Milliseconds())
	if *min > 0 && rate < *min {
		fmt.Println("RØD: under terskelen")
		os.Exit(1)
	}
}

func readJSONL[T any](path string) []T {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			fmt.Fprintln(os.Stderr, "jsonl:", err)
			os.Exit(2)
		}
		out = append(out, v)
	}
	return out
}
