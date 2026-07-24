package intent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ScalewayEmbedder embedder tekster via Scaleways OpenAI-kompatible
// /embeddings-endepunkt (samme upstream som resten av appen).
type ScalewayEmbedder struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (s *ScalewayEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": s.Model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(s.BaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings-kall: status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings-kall: fikk %d vektorer for %d tekster", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("embeddings-kall: ugyldig index %d", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

// ScalewayJudge velger én nøkkel blant kandidatene med et minimalt,
// enum-begrenset chat-kall. Svarer modellen utenfor listen, håndterer
// motoren det (fallback) — dommeren stoles aldri blindt på.
type ScalewayJudge struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (s *ScalewayJudge) Pick(ctx context.Context, message string, keys []string) (string, error) {
	// Dommeren får beskrivelsene, ikke bare nøkkelnavnene — nakne nøkler som
	// «data_question» lyder riktige for verdensfakta og ga systematiske bom.
	var b strings.Builder
	b.WriteString("Velg hvilken flyt brukerens melding gjelder. " +
		"Velg etter HANDLINGEN brukeren ber om, ikke temaet: " +
		"«varsle meg når kursen …» er en rutine (handling: overvåke), ikke et faktaspørsmål, " +
		"og «lag en graf over oljeprisen» er en widget (handling: lage graf).\n")
	for _, k := range keys {
		switch {
		case k == FreeChatKey:
			fmt.Fprintf(&b, "- %s: ingen av disse — vanlig samtale, tekstarbeid på innhold brukeren gir, eller sammensatte ønsker\n", k)
		default:
			if in, ok := byKey[k]; ok {
				fmt.Fprintf(&b, "- %s: %s\n", k, in.Description)
			} else {
				fmt.Fprintf(&b, "- %s\n", k)
			}
		}
	}
	b.WriteString("Svar KUN med én av nøklene, ingenting annet: " + strings.Join(keys, ", "))
	system := b.String()
	body, _ := json.Marshal(map[string]any{
		"model": s.Model,
		"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": message},
		},
		"stream":      false,
		"temperature": 0,
		"max_tokens":  16,
		"reasoning":   map[string]any{"enabled": false},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(s.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dommer-kall: status %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("dommer-kall: tomt svar")
	}
	ans := strings.ToLower(strings.TrimSpace(out.Choices[0].Message.Content))
	// Vær raus med formen (punktum, anførselstegn) men aldri med innholdet.
	ans = strings.Trim(ans, "«»\"'.,! ")
	for _, k := range keys {
		if ans == k {
			return k, nil
		}
	}
	return "", fmt.Errorf("dommer-kall: %q er ikke blant kandidatene", ans)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
