// intent-eval kjører hele intent-motoren (embeddings + dommer) mot
// fasit-settet i internal/intent/testdata/eval.jsonl og feiler under terskel.
// Dette er porten: ingen register-endring merges uten grønn kjøring.
//
//	go run ./cmd/intent-eval            # full kjøring mot live Mistral
//	go run ./cmd/intent-eval -verbose   # + hver enkelt avgjørelse
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	client := &http.Client{Timeout: 60 * time.Second, Transport: &retry{http.DefaultTransport}}
	embedder := &intent.MistralEmbedder{
		BaseURL: cfg.UpstreamBaseURL, APIKey: cfg.UpstreamAPIKey,
		Model: "mistral-embed", Client: client,
	}
	judge := &intent.MistralJudge{
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
	// Degraderte avgjørelser (embedding/dommer feilet — typisk timeout) sier
	// ingenting om ruting. De holdes UTENFOR accuracy, ellers måler porten
	// nettverket: identisk kode ga 115-119 av 131 og krysset 90 %-grensen
	// begge veier. Rapporteres for seg — de skal være få.
	var degraded int
	var lat []time.Duration
	methods := map[string]int{}
	var misses []string
	// Uklart måles for seg: den forrige oppklaringsmekanismen døde usett
	// fordi ingenting talte den. Falske positive er den viktige halvdelen —
	// en assistent som spør i stedet for å svare er verre enn ingen.
	var unclearWant, unclearHit, unclearFalse int
	// Pause mellom kall: porten skal måle RUTINGSKVALITET, ikke
	// leverandørens rate-limit. Uten den fyrer evalen 147 kall på rappen,
	// Mistral svarer 429 (målt: 93 av 147), og både accuracy og latens
	// beskriver da burst-oppførsel ingen bruker utsettes for — i prod
	// kommer ett kall om gangen fra én person.
	const pace = 250 * time.Millisecond
	for i, c := range cases {
		if i > 0 {
			time.Sleep(pace)
		}
		d := engine.Resolve(context.Background(), c.Text, true)
		// Ett omforsøk på degraderte kall. Uten dette forsvant tilfeller ut av
		// nevneren når embedding-kallet timet ut, og settet krympet fra
		// kjøring til kjøring (131, 130, 126) — vi målte aldri hele suiten,
		// og de tregeste tilfellene falt systematisk ut først.
		if d.Degraded {
			d = engine.Resolve(context.Background(), c.Text, true)
		}
		lat = append(lat, d.Elapsed)
		methods[d.Method]++
		norm := func(k string) string {
			if k == "free_chat" {
				return ""
			}
			return k
		}
		if d.Degraded {
			degraded++
			if *verbose {
				fmt.Printf("SKIP  degradert (kall feilet)          «%s»\n", c.Text)
			}
			continue
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
	scored := len(cases) - degraded
	if scored == 0 {
		fmt.Fprintln(os.Stderr, "alle avgjørelser degraderte — upstream nede?")
		os.Exit(2)
	}
	acc := float64(hits) / float64(scored)
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
	// Én desimal: «90 %» ved 89,9 % fikk rapporten til å motsi porten.
	fmt.Printf("Accuracy: %d/%d (%.1f %%)   metoder: %s\n",
		hits, scored, 100*acc, strings.Join(mparts, " "))
	if degraded > 0 {
		fmt.Printf("Degradert: %d av %d holdt utenfor (embedding/dommer feilet)\n", degraded, len(cases))
	}
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

// retry: samme 429-backoff som produksjonsserveren. Uten den måler porten
// leverandørens rate-limit i stedet for rutingen (målt: 93 av 147 kall
// fikk 429 og falt stille tilbake på embedding-scoren).
type retry struct{ base http.RoundTripper }

func (t *retry) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body.Close()
		body = b
	}
	delays := []time.Duration{400 * time.Millisecond, 1200 * time.Millisecond, 3 * time.Second}
	for attempt := 0; ; attempt++ {
		r := req.Clone(req.Context())
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		resp, err := t.base.RoundTrip(r)
		if err == nil && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return resp, nil
		}
		if attempt >= len(delays) {
			return resp, err
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(delays[attempt])
	}
}
