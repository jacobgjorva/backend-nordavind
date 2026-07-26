package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pricing holder pris per token (USD) per modell, hentet fra Scaleway.
type pricing struct {
	mu    sync.RWMutex
	perTk map[string][2]float64 // model -> [input, output]
}

// fallbackPerTk: Scaleways offisielle listepriser (EUR/token, omregnet til USD
// med fast kurs 1.08) — Scaleway sender IKKE priser i /models (eurouter
// gjorde), så uten denne tabellen ble all kostnad stående på 0.
// Kilde: scaleway.com/en/pricing/model-as-a-service, hentet 2026-07-25.
var fallbackPerTk = map[string][2]float64{
	"mistral-medium-3.5-128b":             {1.62e-6, 8.10e-6},
	"qwen3.5-397b-a17b":                   {0.648e-6, 3.888e-6},
	"glm-5.2":                             {1.944e-6, 5.94e-6},
	"qwen3.6-35b-a3b":                     {0.27e-6, 1.62e-6},
	"qwen3-embedding-8b":                  {0.108e-6, 0},
	"mistral-small-3.2-24b-instruct-2506": {0.162e-6, 0.378e-6},
	"llama-3.3-70b-instruct":              {0.972e-6, 0.972e-6},
	"gemma-3-27b-it":                      {0.27e-6, 0.54e-6},
}

func newPricing() *pricing {
	perTk := make(map[string][2]float64, len(fallbackPerTk))
	for k, v := range fallbackPerTk {
		perTk[k] = v
	}
	return &pricing{perTk: perTk}
}

// load henter prislisten; kalles i bakgrunnen ved oppstart og hver 6. time.
func (p *pricing) load(ctx context.Context, client *http.Client, baseURL, apiKey string) error {
	url := strings.TrimSuffix(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var body struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}

	// Start fra fallback-tabellen og la upstream KUN overstyre der den faktisk
	// oppgir priser — Scaleway sender ingen, og da skal listeprisene stå.
	next := make(map[string][2]float64, len(fallbackPerTk)+len(body.Data))
	for k, v := range fallbackPerTk {
		next[k] = v
	}
	for _, m := range body.Data {
		in, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
		out, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
		if in > 0 || out > 0 {
			next[m.ID] = [2]float64{in, out}
		}
	}
	p.mu.Lock()
	p.perTk = next
	p.mu.Unlock()
	return nil
}

func (p *pricing) refreshLoop(client *http.Client, baseURL, apiKey string) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		p.load(ctx, client, baseURL, apiKey)
		cancel()
		time.Sleep(6 * time.Hour)
	}
}

// cost beregner USD for et forbruk. Modellnavn kan ha leverandørprefiks.
func (p *pricing) cost(model string, promptTokens, completionTokens int) float64 {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	rate, ok := p.perTk[model]
	if !ok {
		return 0
	}
	return float64(promptTokens)*rate[0] + float64(completionTokens)*rate[1]
}
