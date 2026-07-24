package intent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
)

// Metode-konstanter for hvordan et valg ble tatt — logges og måles.
const (
	MethodDirect = "direct" // klar vinner på ren cosine (ingen LLM)
	MethodJudge  = "judge"  // enum-begrenset dommer-kall blant kandidatene
	MethodNone   = "none"   // ingen flyt — fri chat (også ved feil: fail-open)
	MethodMulti  = "multi"  // sammensatt ønske — to flyter nesten likt → fri chat
)

// Terskler. Kalibrert mot eval-settet (cmd/intent-eval skriver ut forslag);
// endres KUN sammen med en grønn eval-kjøring — aldri på magefølelse.
const (
	// floorScore: under dette ligner meldingen ikke på noen flyt → fri chat.
	floorScore = 0.42
	// directScore + directMargin: over dette OG med klar avstand til nummer
	// to velger matten alene, uten dommer.
	directScore  = 0.60
	directMargin = 0.08
	// candN: hvor mange kandidater dommeren får velge blant.
	candN = 3
	// multiMargin: scorer to flyter BEGGE over directScore med mindre avstand
	// enn dette, tolkes meldingen som sammensatt («lag graf OG eksporter») og
	// går til fri chat som har alle verktøy. Logges som MethodMulti.
	multiMargin = 0.03
	// embedTimeout/judgeTimeout: motoren skal aldri henge — fail-open.
	embedTimeout = 6 * time.Second
	judgeTimeout = 5 * time.Second
)

// Embedder gjør tekster om til vektorer. Injiseres (Scaleway i produksjon,
// stub i tester).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Judge velger én nøkkel blant kandidatene for en melding. Injiseres (lite
// LLM-kall i produksjon, stub i tester). Skal returnere en av keys.
type Judge interface {
	Pick(ctx context.Context, message string, keys []string) (string, error)
}

// Candidate er en scoret flyt-kandidat.
type Candidate struct {
	Key   string  `json:"key"`
	Score float64 `json:"score"`
}

// Decision er motorens svar for én melding.
type Decision struct {
	// Key er valgt flyt, eller "" ved MethodNone (fri chat).
	Key        string      `json:"key"`
	Method     string      `json:"method"`
	Candidates []Candidate `json:"candidates"`
	// Elapsed er total tid brukt i motoren (embed + evt. dommer).
	Elapsed time.Duration `json:"-"`
}

// entry er én embeddet tekst (beskrivelse eller eksempel) for en intent.
type entry struct {
	key string
	vec []float32
}

// Engine holder de forhåndsembeddede nøklene og tar avgjørelser.
type Engine struct {
	embedder Embedder
	judge    Judge
	log      *slog.Logger
	entries  []entry
	// RegistryHash identifiserer registerinnholdet — brukes av vektorcachen
	// så vi kun re-embedder når registeret faktisk er endret.
	RegistryHash string
}

// RegistryTexts er tekstene som skal embeddes, i stabil rekkefølge, med
// tilhørende nøkler. Eksponert så oppstartskoden kan gjenbruke cachede
// vektorer (samme rekkefølge = samme indeksering).
func RegistryTexts() (keys []string, texts []string) {
	for _, in := range Registry {
		keys = append(keys, in.Key)
		texts = append(texts, in.Description)
		for _, ex := range in.Examples {
			keys = append(keys, in.Key)
			texts = append(texts, ex)
		}
	}
	return keys, texts
}

// HashRegistry gir en stabil hash av hele registerinnholdet.
func HashRegistry() string {
	_, texts := RegistryTexts()
	h := sha256.Sum256([]byte(strings.Join(texts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// New bygger motoren. vectors kan komme fra cache (samme rekkefølge som
// RegistryTexts); er den nil, embeddes registeret nå.
func New(ctx context.Context, embedder Embedder, judge Judge, log *slog.Logger, vectors [][]float32) (*Engine, error) {
	keys, texts := RegistryTexts()
	if vectors == nil {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var err error
		vectors, err = embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding av registeret: %w", err)
		}
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("vektorcache ute av synk: %d vektorer, %d tekster", len(vectors), len(texts))
	}
	e := &Engine{embedder: embedder, judge: judge, log: log, RegistryHash: HashRegistry()}
	for i, k := range keys {
		e.entries = append(e.entries, entry{key: k, vec: vectors[i]})
	}
	return e, nil
}

// Vectors returnerer registervektorene (til cache-lagring).
func (e *Engine) Vectors() [][]float32 {
	out := make([][]float32, len(e.entries))
	for i, en := range e.entries {
		out[i] = en.vec
	}
	return out
}

// Resolve tar avgjørelsen for én melding. isAdmin filtrerer AdminOnly-flyter.
// Feiler noe som helst, returneres MethodNone — motoren blokkerer aldri.
func (e *Engine) Resolve(ctx context.Context, message string, isAdmin bool) Decision {
	start := time.Now()
	none := Decision{Method: MethodNone}

	msg := strings.TrimSpace(message)
	if msg == "" || len(msg) > 2000 {
		none.Elapsed = time.Since(start)
		return none
	}

	ectx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	vecs, err := e.embedder.Embed(ectx, []string{msg})
	if err != nil || len(vecs) != 1 {
		// Ett nytt forsøk — embed-kall har målbare p95-outliere.
		ectx2, cancel2 := context.WithTimeout(ctx, embedTimeout)
		defer cancel2()
		vecs, err = e.embedder.Embed(ectx2, []string{msg})
		if err != nil || len(vecs) != 1 {
			e.log.Warn("intent: embedding feilet, fail-open til fri chat", "err", err)
			none.Elapsed = time.Since(start)
			return none
		}
	}
	qv := vecs[0]

	// Beste score per nøkkel (maks over beskrivelse + eksempler).
	best := map[string]float64{}
	for _, en := range e.entries {
		if in, ok := byKey[en.key]; ok && in.AdminOnly && !isAdmin {
			continue
		}
		if s := cosine(qv, en.vec); s > best[en.key] {
			best[en.key] = s
		}
	}
	cands := make([]Candidate, 0, len(best))
	for k, s := range best {
		cands = append(cands, Candidate{Key: k, Score: s})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	if len(cands) > candN {
		cands = cands[:candN]
	}

	// Under gulvet: dette er fri chat.
	if len(cands) == 0 || cands[0].Score < floorScore {
		none.Candidates = cands
		none.Elapsed = time.Since(start)
		return none
	}

	// Sammensatt ønske: to flyter nesten likt, begge sterke → fri chat.
	if len(cands) > 1 && cands[1].Score >= directScore &&
		cands[0].Score-cands[1].Score < multiMargin {
		return Decision{Method: MethodMulti, Candidates: cands, Elapsed: time.Since(start)}
	}

	// Klar vinner: matten bestemmer alene.
	if cands[0].Score >= directScore &&
		(len(cands) == 1 || cands[0].Score-cands[1].Score >= directMargin) {
		return Decision{Key: cands[0].Key, Method: MethodDirect, Candidates: cands, Elapsed: time.Since(start)}
	}

	// Usikkert: dommeren velger blant kandidatene — og KUN blant dem.
	keys := make([]string, len(cands))
	for i, c := range cands {
		keys[i] = c.Key
	}
	jctx, jcancel := context.WithTimeout(ctx, judgeTimeout)
	defer jcancel()
	pick, err := e.judge.Pick(jctx, msg, keys)
	if err != nil || !contains(keys, pick) {
		// Dommeren feilet eller svarte utenfor enum: fall tilbake til beste
		// kandidat hvis den er godt over gulvet, ellers fri chat.
		if cands[0].Score >= directScore {
			return Decision{Key: cands[0].Key, Method: MethodDirect, Candidates: cands, Elapsed: time.Since(start)}
		}
		e.log.Warn("intent: dommer feilet, fail-open til fri chat", "err", err)
		none.Candidates = cands
		none.Elapsed = time.Since(start)
		return none
	}
	return Decision{Key: pick, Method: MethodJudge, Candidates: cands, Elapsed: time.Since(start)}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// MarshalCandidates gir kandidatlisten som JSON (til logging).
func MarshalCandidates(cs []Candidate) string {
	b, _ := json.Marshal(cs)
	return string(b)
}
