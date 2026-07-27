// intent-eval kjører hele intent-motoren (embeddings + dommer) mot
// fasit-settet i internal/intent/testdata/eval.jsonl og feiler under terskel.
// Dette er porten: ingen register-endring merges uten grønn kjøring.
//
//	go run ./cmd/intent-eval            # full kjøring mot live Scaleway
//	go run ./cmd/intent-eval -verbose   # + hver enkelt avgjørelse
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
	"sort"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

// Terskler for grønn kjøring. Skjerpes etter hvert som settet vokser —
// aldri senkes uten eksplisitt avtale.
const (
	minAccuracy = 0.90
	maxP95      = 2500 * time.Millisecond
)

type caseLine struct {
	Text string `json:"text"`
	Want string `json:"want"`
}

func main() {
	verbose := flag.Bool("verbose", false, "skriv hver avgjørelse")
	file := flag.String("file", "internal/intent/testdata/eval.jsonl", "fasit-sett (jsonl)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	embedder := &intent.ScalewayEmbedder{
		BaseURL: cfg.UpstreamBaseURL, APIKey: cfg.UpstreamAPIKey,
		Model: "qwen3-embedding-8b", Client: client,
	}
	judge := &intent.ScalewayJudge{
		BaseURL: cfg.UpstreamBaseURL, APIKey: cfg.UpstreamAPIKey,
		Model: router.MidModel, Client: client,
	}

	t0 := time.Now()
	engine, err := intent.New(context.Background(), embedder, judge, slog.Default(), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "motor:", err)
		os.Exit(2)
	}
	fmt.Printf("Register embeddet på %s (hash %s)\n\n", time.Since(t0).Round(10*time.Millisecond), engine.RegistryHash)

	f, err := os.Open(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fasit-sett:", err)
		os.Exit(2)
	}
	defer f.Close()

	var cases []caseLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var c caseLine
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			fmt.Fprintln(os.Stderr, "ugyldig linje i eval.jsonl:", err)
			os.Exit(2)
		}
		cases = append(cases, c)
	}

	var hits int
	var lat []time.Duration
	methods := map[string]int{}
	var misses []string
	// Uklart måles for seg: den forrige oppklaringsmekanismen døde usett
	// fordi ingenting talte den. Falske positive er den viktige halvdelen —
	// en assistent som spør i stedet for å svare er verre enn ingen.
	var unclearWant, unclearHit, unclearFalse int
	for _, c := range cases {
		d := engine.Resolve(context.Background(), c.Text, true)
		lat = append(lat, d.Elapsed)
		methods[d.Method]++
		norm := func(k string) string {
			if k == "free_chat" {
				return ""
			}
			return k
		}
		ok := norm(d.Key) == norm(c.Want)
		if c.Want == intent.UnclearKey {
			unclearWant++
			if ok {
				unclearHit++
			}
		} else if d.Key == intent.UnclearKey {
			unclearFalse++
		}
		if ok {
			hits++
		} else {
			misses = append(misses, fmt.Sprintf("  «%s» → fasit %s, fikk %s (%s) %s",
				c.Text, c.Want, orNone(d.Key), d.Method, intent.MarshalCandidates(d.Candidates)))
		}
		if *verbose {
			fmt.Printf("%-5v %-18s %-8s %6dms  «%s»\n", ok, orNone(d.Key), d.Method, d.Elapsed.Milliseconds(), c.Text)
		}
	}

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	acc := float64(hits) / float64(len(cases))
	p50 := lat[len(lat)/2]
	p95 := lat[int(float64(len(lat))*0.95)-1]

	var mkeys []string
	for m := range methods {
		mkeys = append(mkeys, m)
	}
	sort.Strings(mkeys)
	var mparts []string
	for _, m := range mkeys {
		mparts = append(mparts, fmt.Sprintf("%s=%d", m, methods[m]))
	}
	fmt.Printf("Accuracy: %d/%d (%.0f %%)   metoder: %s\n",
		hits, len(cases), 100*acc, strings.Join(mparts, " "))
	fmt.Printf("Uklart:   %d/%d truffet, %d falskt utløst\n", unclearHit, unclearWant, unclearFalse)
	fmt.Printf("Latens:   p50 %dms, p95 %dms\n", p50.Milliseconds(), p95.Milliseconds())
	if len(misses) > 0 {
		fmt.Println("\nBom:")
		for _, m := range misses {
			fmt.Println(m)
		}
	}

	if acc < minAccuracy || p95 > maxP95 {
		fmt.Printf("\nRØD: krav er accuracy ≥ %.0f %% og p95 ≤ %s\n", 100*minAccuracy, maxP95)
		os.Exit(1)
	}
	fmt.Println("\nGRØNN")
}

func orNone(k string) string {
	if k == "" {
		return "(fri chat)"
	}
	return k
}
