package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

const (
	embeddingModel    = "qwen3-embedding-8b"
	knowledgeTopK     = 6    // antall mest relevante noder
	knowledgeMinScore = 0.35 // ignorer svake treff
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

// knowledgeContext bygger en kompakt kontekst-blokk av de mest relevante
// bransje-nodene for spørsmålet, pluss deres nærmeste naboer. Returnerer
// tom streng hvis tenanten ingen relevante noder har.
func (s *Server) knowledgeContext(ctx context.Context, tenantID, query string) string {
	if strings.TrimSpace(query) == "" {
		return ""
	}
	nodes, err := s.store.AcceptedNodes(tenantID)
	if err != nil || len(nodes) == 0 {
		return ""
	}
	qvec, err := s.embed(ctx, query)
	if err != nil {
		s.log.Warn("embedding feilet", "err", err)
		return ""
	}

	type scored struct {
		node  store.KnowledgeNode
		score float64
	}
	ranked := make([]scored, 0, len(nodes))
	for _, n := range nodes {
		ranked = append(ranked, scored{n, cosine(qvec, n.Embedding)})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	picked := map[string]store.KnowledgeNode{}
	var ids []string
	for _, r := range ranked {
		if len(picked) >= knowledgeTopK || r.score < knowledgeMinScore {
			break
		}
		picked[r.node.ID] = r.node
		ids = append(ids, r.node.ID)
	}
	if len(picked) == 0 {
		return ""
	}

	// 1-hopp naboer utvider konteksten langs grafen.
	if neighbors, err := s.store.NeighborSummaries(tenantID, ids); err == nil {
		for _, n := range neighbors {
			if _, ok := picked[n.ID]; !ok {
				picked[n.ID] = n
			}
		}
	}

	var b strings.Builder
	b.WriteString("Relevant bransjekunnskap om denne bedriften (bruk der det passer, ikke nevn at du har den):\n")
	for _, n := range picked {
		fmt.Fprintf(&b, "- %s: %s\n", n.Title, n.Summary)
	}
	return b.String()
}
