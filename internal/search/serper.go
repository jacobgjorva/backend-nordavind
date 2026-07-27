package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Serper: Googles resultater via API. Primærkilde NÅR nøkkel er satt.
//
// Bakgrunn: prod-IP-en avvises av praktisk talt alle søkemotorer (målt — kun
// mojeek svarer, og den rate-limiter etter ~10 raske søk). Et API omgår hele
// blokkeringsproblemet fordi vi ikke lenger scraper fra en datasenter-IP.
// Serper er valgt på pris: ~$0,30-1,00 per 1000 spørringer mot Braves ~$5.
//
// SUVERENITET: dette sender søkestrengen til en amerikansk leverandør.
// Kundedata (dokumenter, database, e-post) forlater ALDRI EU — kun selve
// søkestrengen går ut, og bare når Wikipedia-laget ikke dekker spørsmålet.
// Skal stå eksplisitt i DPA-en, ikke skjules.
//
// Tom nøkkel = hele veien er av, og søket går som før (SearXNG → DDG).

const serperEndpoint = "https://google.serper.dev/search"

// serperSearch spør Serper. Fail-open: enhver feil returneres til Search, som
// faller videre til SearXNG/DDG.
func (c *Client) serperSearch(ctx context.Context, query string) ([]Result, error) {
	body, _ := json.Marshal(map[string]any{
		"q":   query,
		"gl":  "no", // geografisk vekting mot Norge; språket følger spørringen
		"num": 10,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serperURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", c.serperKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("serper: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
		// answerBox og knowledgeGraph bærer ofte selve faktumet (fødselsdato,
		// høyde, grunnlegger) uten at siden må hentes — tas med som egen kilde.
		AnswerBox struct {
			Title   string `json:"title"`
			Answer  string `json:"answer"`
			Snippet string `json:"snippet"`
			Link    string `json:"link"`
		} `json:"answerBox"`
		KnowledgeGraph struct {
			Title       string `json:"title"`
			Type        string `json:"type"`
			Description string `json:"description"`
			Link        string `json:"link"`
		} `json:"knowledgeGraph"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("serper: ugyldig JSON: %w", err)
	}

	var results []Result
	// Direktesvaret først når det finnes — det er ofte hele svaret.
	if a := out.AnswerBox; a.Answer != "" || a.Snippet != "" {
		txt := a.Answer
		if txt == "" {
			txt = a.Snippet
		}
		results = append(results, Result{
			Title:       orFallback(a.Title, "Direktesvar"),
			URL:         a.Link,
			Description: txt,
		})
	}
	if k := out.KnowledgeGraph; k.Description != "" {
		results = append(results, Result{
			Title:       orFallback(k.Title, k.Type),
			URL:         k.Link,
			Description: k.Description,
		})
	}
	for _, o := range out.Organic {
		if o.Link == "" {
			continue
		}
		results = append(results, Result{Title: o.Title, URL: o.Link, Description: o.Snippet})
		if len(results) >= 8 {
			break
		}
	}
	return results, nil
}

func orFallback(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
