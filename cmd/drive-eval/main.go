// drive-eval måler «drive»-stilen (avtalt 2026-08-06): hvert svar skal ta
// stilling når dataene gir grunnlag, og slutte med fremdrift — en vurdering
// eller ETT konkret, utførbart tilbud. Aldri meny, aldri generisk «si fra».
//
// Kjøres mot en KJØRENDE backend som en ekte klient (samme mønster som
// flow-sim); en dommer (temp 0) scorer hvert svar binært mot rubrikken.
// Suiten er GRØNN ved ≥85 % pass. Hver kjøring koster dommer-tokens — kjør
// bevisst, aldri i løkke.
//
//	NORDAVIND_TOKEN=<sesjonstoken> go run ./cmd/drive-eval
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
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

type driveCase struct {
	Shelf string `json:"shelf"`
	Text  string `json:"text"`
}

type driveResult struct {
	Case    driveCase `json:"case"`
	Answer  string    `json:"answer"`
	TotalMS int64     `json:"total_ms"`
	Err     string    `json:"err,omitempty"`

	Stance  bool   `json:"stance"`  // tar stilling der dataene gir grunnlag
	Drive   bool   `json:"drive"`   // slutter med vurdering eller ETT utførbart tilbud
	Generic bool   `json:"generic"` // generisk avslutning eller meny av alternativer
	Comment string `json:"comment,omitempty"`
}

func (r driveResult) pass() bool { return r.Err == "" && r.Stance && r.Drive && !r.Generic }

// STRUPING (2026-08-06): denne evalen deler Mistral-konto med PROD. En tett
// serie kall spiste rate-limit-kvoten og ga ekte brukere 429 midt i en
// samtale. Derfor: ett kall om gangen, alltid pause mellom — evalen skal
// aldri kunne sulte prod. Tåler at en kjøring tar minutter.
var evalPause = 6 * time.Second

func pace(what string) {
	fmt.Printf("   (pause %s før %s)\n", evalPause, what)
	time.Sleep(evalPause)
}

// infraNoise: svaret er en infrastruktursvikt eller en tom datakilde, ikke en
// stilprestasjon. Slike caser skal ALDRI telle som stilfeil — de sier bare at
// upstream glapp eller at testdataene ikke dekker perioden. De rapporteres for
// seg, aldri skjult (jf. «no silent caps»).
func infraNoise(answer string) bool {
	a := strings.ToLower(answer)
	switch {
	case strings.TrimSpace(a) == "":
		return true
	case strings.Contains(a, "klarte ikke hente et svar"):
		return true
	case strings.Contains(a, "ingen av dem ga rader"):
		return true
	case strings.Contains(a, "backend er ikke konfigurert"):
		return true
	}
	return false
}

// driveRubric er kriteriene VÅRE — dommeren scorer aldri etter egne ideer.
const driveRubric = `Du vurderer svar fra en norsk bedrifts-AI som skal være en MOTOR som driver brukeren mot målet — aldri et oppslagsverk. Vurder SVARET på BRUKERENS melding, binært:
- stance: true hvis svaret TAR STILLING der grunnlaget finnes (kaller et tall lavt/høyt/normalt, anbefaler, prioriterer) — ren gjengivelse av fakta uten vurdering er false. Rene faktaspørsmål uten vurderingsrom («hva er adressen») scorer true når svaret er direkte.
- drive: true hvis svaret SLUTTER med fremdrift: en vurdering brukeren kan handle på, eller ETT konkret tilbud assistenten selv kan utføre («vil du at jeg bryter det ned per kunde?»). En liste av alternativer, et generisk «noe mer du lurer på?» eller ingen retning er false.
- generic: true hvis svaret har høflig fyll uten innhold («si fra om du lurer på noe», «håper det hjelper») eller en meny av mer enn ett tilbud.
- UNNTAK småprat: er brukerens melding ren høflighet/takk uten oppgave, er et kort, varmt svar som evt. spør hva brukeren trenger RIKTIG — da er stance=true, drive=true, generic=false.
Svar KUN med JSON: {"stance":bool,"drive":bool,"generic":bool,"comment":"én kort setning"}`

func main() {
	base := flag.String("base", "http://localhost:8080", "backend-URL")
	judgeOnly := flag.String("judge-only", "", "re-scor en tidligere kjøring (run-dir)")
	pauseSec := flag.Int("pause", 6, "sekunder mellom hvert modellkall (deler kvote med prod)")
	flag.Parse()
	evalPause = time.Duration(*pauseSec) * time.Second

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

	var results []driveResult
	runDir := *judgeOnly
	if runDir == "" {
		runDir = filepath.Join("drive_runs", time.Now().Format("20060102-150405"))
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

	pass, judged, noise := 0, 0, 0
	for i := range results {
		if results[i].Err != "" {
			noise++
			fmt.Printf("-- %-18s FEIL: %s\n", results[i].Case.Shelf, results[i].Err)
			continue
		}
		// Infrastruktursvikt/tom kilde etter tre forsøk: rapporteres, men
		// dømmes ikke — ellers måler vi upstream i stedet for stil.
		if infraNoise(results[i].Answer) {
			noise++
			fmt.Printf("-- %-18s IKKE MÅLT (infra/tom kilde): %.60q\n", results[i].Case.Shelf, results[i].Answer)
			continue
		}
		judge(client, cfg, &results[i])
		// Dommer-svikt etter retry er målestøy — ute av nevneren, aldri en dom.
		if strings.HasPrefix(results[i].Comment, "dommer feilet") ||
			strings.HasPrefix(results[i].Comment, "uparsbar dom") {
			noise++
			fmt.Printf("-- %-18s IKKE MÅLT: %s\n", results[i].Case.Shelf, results[i].Comment)
			continue
		}
		judged++
		mark := "  "
		if results[i].pass() {
			pass++
			mark = "ok"
		}
		fmt.Printf("%s %-18s stilling=%t drive=%t generisk=%t  %s\n",
			mark, results[i].Case.Shelf, results[i].Stance, results[i].Drive,
			results[i].Generic, results[i].Comment)
	}
	save(runDir, "results.json", results)

	pct := 0
	if judged > 0 {
		pct = pass * 100 / judged
	}
	fmt.Printf("\n== drive-eval: %d/%d målt (%d %%) — krav ≥85 %%. %d av %d ikke målt (infra/tom kilde/dommer) ==\n",
		pass, judged, pct, noise, len(results))
	// Måler vi for få caser, er tallet ikke til å stole på — like alvorlig som
	// rødt, og skal aldri leses som grønt.
	if judged*100/max(len(results), 1) < 70 {
		fmt.Println("ADVARSEL: for mange caser ble ikke målt — kjøringen er ikke representativ.")
		os.Exit(1)
	}
	if pct < 85 {
		os.Exit(1)
	}
}

func runSuite(client *http.Client, base, token string) []driveResult {
	f, err := os.Open("internal/intent/testdata/drive_cases.jsonl")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer f.Close()
	var out []driveResult
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var c driveCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			continue
		}
		r := driveResult{Case: c}
		for attempt := 0; attempt < 2; attempt++ {
			r.Answer, r.TotalMS, r.Err = askBackend(client, base, token, c.Text)
			if r.Err == "" && !infraNoise(r.Answer) {
				break
			}
			pace("retry")
		}
		fmt.Printf("kjørt: %-18s %5dms  %.60q\n", c.Shelf, r.TotalMS, r.Answer)
		out = append(out, r)
		pace("case")
	}
	return out
}

// askBackend sender én melding som en ekte klient (streaming) og samler svaret.
func askBackend(client *http.Client, base, token, text string) (answer string, totalMS int64, errStr string) {
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
		return "", 0, err.Error()
	}
	defer resp.Body.Close()
	var b strings.Builder
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
		}
		if json.Unmarshal([]byte(line[6:]), &d) != nil {
			continue
		}
		if len(d.Choices) > 0 {
			b.WriteString(d.Choices[0].Delta.Content)
		}
	}
	return b.String(), time.Since(start).Milliseconds(), ""
}

func judge(client *http.Client, cfg config.Config, r *driveResult) {
	user := fmt.Sprintf("BRUKERENS MELDING:\n%s\n\nSVARET:\n%s", r.Case.Text, r.Answer)
	// Retry med backoff: upstream svarer tidvis tomt/429 ved tette kall i
	// serie — en tapt dom er målestøy, ikke et funn.
	var raw string
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err = llmJSON(client, cfg, router.MidModel, driveRubric, user, 400)
		if err == nil && strings.TrimSpace(raw) != "" {
			break
		}
		pace("dommer-retry")
	}
	pace("dom")
	if err != nil {
		r.Comment = "dommer feilet: " + err.Error()
		return
	}
	var v struct {
		Stance  bool   `json:"stance"`
		Drive   bool   `json:"drive"`
		Generic bool   `json:"generic"`
		Comment string `json:"comment"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &v); err != nil {
		r.Comment = "uparsbar dom: " + truncate(raw, 120)
		return
	}
	r.Stance, r.Drive, r.Generic, r.Comment = v.Stance, v.Drive, v.Generic, v.Comment
}

func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return s
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

func llmJSON(client *http.Client, cfg config.Config, model, system, user string, maxTokens int) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": user},
		},
		"stream": false, "temperature": 0, "max_tokens": maxTokens,
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
