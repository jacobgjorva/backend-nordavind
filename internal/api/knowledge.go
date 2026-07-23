package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

const (
	embeddingModel = "qwen3-embedding-8b"
	// qwen3-embedding scorer urelatert tekst ~0.21 og klart relevant ~0.31–0.53.
	// 0.28 er vektor-gulvet som skiller signal fra støy.
	vecFloor    = 0.28
	candDepth   = 30   // hvor mange kandidater fra hver kilde (vektor / nøkkelord)
	rrfK        = 60.0 // RRF-konstant (standard); demper topprangeringens dominans
	noteMaxN    = 25   // maks antall lapper (budsjettet er den reelle grensen)
	noteBudget  = 4000 // hardt tak på injisert tekst (tegn)
)

// knowledgeFor henter kunnskapskonteksten for siste brukermelding i en
// forespørsel, hvis brukeren er innlogget og tenanten har noder.
func (s *Server) knowledgeFor(ctx context.Context, full map[string]any) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok || user.TenantID == "" {
		return ""
	}
	return s.knowledgeContext(ctx, user.TenantID, lastUserText(full))
}

// lastUserText henter ren tekst fra siste brukermelding (håndterer både
// streng-innhold og multimodale deler).
func lastUserText(full map[string]any) string {
	msgs, _ := full["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			var b strings.Builder
			for _, part := range c {
				if pm, ok := part.(map[string]any); ok && pm["type"] == "text" {
					if t, ok := pm["text"].(string); ok {
						b.WriteString(t)
						b.WriteString(" ")
					}
				}
			}
			return b.String()
		}
		return ""
	}
	return ""
}

// embed lager en vektor for en tekst via Scaleways embeddings-endepunkt.
// Global cache for query-embeddings. Embeddings er tenant-uavhengige (samme
// tekst → samme vektor), så identiske spørsmål slipper et nytt kall.
var (
	embedCache  sync.Map // string -> []float32
	embedCacheN atomic.Int64
)

const embedCacheMax = 1000

// embedCached returnerer en cachet embedding for teksten, ellers henter og
// cacher den (bundet størrelse).
func (s *Server) embedCached(ctx context.Context, text string) ([]float32, error) {
	key := strings.ToLower(strings.TrimSpace(text))
	if v, ok := embedCache.Load(key); ok {
		return v.([]float32), nil
	}
	vec, err := s.embed(ctx, text)
	if err != nil {
		return nil, err
	}
	if embedCacheN.Load() < embedCacheMax {
		if _, loaded := embedCache.LoadOrStore(key, vec); !loaded {
			embedCacheN.Add(1)
		}
	}
	return vec, nil
}

func (s *Server) embed(ctx context.Context, text string) ([]float32, error) {
	url := strings.TrimSuffix(s.cfg.UpstreamBaseURL, "/") + "/embeddings"
	body, _ := json.Marshal(map[string]any{"model": embeddingModel, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.UpstreamAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.UpstreamAPIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("tom embedding")
	}
	return out.Data[0].Embedding, nil
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

var ftsTokenRe = regexp.MustCompile(`\p{L}[\p{L}\p{N}]+`)

// ftsQuery bygger et trygt FTS5 MATCH-uttrykk fra fri tekst: plukker ut ord-
// tokens (bokstav-initiale), siterer hver og OR-slår dem. Tåler tegnsetting og
// fanger eksakte koder/navn embeddings bommer på.
func ftsQuery(query string) string {
	toks := ftsTokenRe.FindAllString(strings.ToLower(query), -1)
	parts := make([]string, 0, len(toks))
	for _, t := range toks {
		parts = append(parts, `"`+t+`"`)
	}
	return strings.Join(parts, " OR ")
}

// knowledgeContext henter den mest relevante interne kunnskapen for spørsmålet
// fra den felles lappe-skuffen, som en hybrid av vektor-likhet og nøkkelord
// (BM25), fusjonert med Reciprocal Rank Fusion. Returnerer tom streng når
// ingenting er relevant. Rangeringsbasert fusjon → ingen skjør absoluttterskel.
func (s *Server) knowledgeContext(ctx context.Context, tenantID, query string) string {
	// Svært korte meldinger («ok», «takk») bærer ingen søkbar hensikt.
	if len([]rune(strings.TrimSpace(query))) < 12 {
		return ""
	}
	notes, _ := s.store.AcceptedNotes(tenantID)
	if len(notes) == 0 {
		return ""
	}
	qvec, err := s.embedCached(ctx, query)
	if err != nil {
		s.log.Warn("embedding feilet", "err", err)
		return ""
	}
	byID := make(map[string]store.KnowledgeNote, len(notes))
	for _, n := range notes {
		byID[n.ID] = n
	}

	// Vektor-kandidater: cosine over gulvet, rangert (best først).
	type sc struct {
		id    string
		score float64
	}
	var vec []sc
	for _, n := range notes {
		if c := cosine(qvec, n.Embedding); c >= vecFloor {
			vec = append(vec, sc{n.ID, c})
		}
	}
	sort.Slice(vec, func(i, j int) bool { return vec[i].score > vec[j].score })
	if len(vec) > candDepth {
		vec = vec[:candDepth]
	}

	// Nøkkelord-kandidater (BM25), allerede rangert av SQLite.
	fts, _ := s.store.SearchNotesFTS(tenantID, ftsQuery(query), candDepth)

	// RRF-fusjon: hver liste bidrar 1/(k + rangering).
	rrf := map[string]float64{}
	for rank, v := range vec {
		rrf[v.id] += 1.0 / (rrfK + float64(rank+1))
	}
	for rank, id := range fts {
		rrf[id] += 1.0 / (rrfK + float64(rank+1))
	}
	if len(rrf) == 0 {
		return ""
	}

	fused := make([]string, 0, len(rrf))
	for id := range rrf {
		fused = append(fused, id)
	}
	sort.Slice(fused, func(i, j int) bool {
		if rrf[fused[i]] != rrf[fused[j]] {
			return rrf[fused[i]] > rrf[fused[j]]
		}
		// Lik score: nyeste kunnskap vinner (ferskhet).
		return byID[fused[i]].CreatedAt.After(byID[fused[j]].CreatedAt)
	})

	var b strings.Builder
	b.WriteString("Relevant intern kunnskap (bruk der det passer; siter kilden når du bruker et dokument):\n")
	budget, n := noteBudget, 0
	for _, id := range fused {
		if n >= noteMaxN || budget <= 0 {
			break
		}
		note := byID[id]
		text := note.Text
		if note.Context != "" {
			text = note.Context + " " + text
		}
		src := "notat"
		if note.SourceType == "document" && note.Title != "" {
			src = "«" + note.Title + "»"
		}
		fmt.Fprintf(&b, "- (fra %s) %s\n", src, text)
		budget -= len(text)
		n++
	}
	return b.String()
}
