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

// pricing holder pris per token (USD) per modell.
type pricing struct {
	mu    sync.RWMutex
	perTk map[string][2]float64 // model -> [input, output]
}

// fallbackPerTk: Mistral La Plateforme sine listepriser (USD per token).
// Leverandøren sender ikke priser i /models, så uten denne tabellen ville
// all kostnad stått på 0.
// Kilde: mistral.ai/pricing, hentet 2026-07-30.
var fallbackPerTk = map[string][2]float64{
	"mistral-large-2512": {2e-6, 6e-6},
	"pixtral-large-2411": {2e-6, 6e-6},
	"mistral-medium-2604": {1.5e-6, 7.5e-6},
	"mistral-embed":      {0.1e-6, 0},
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
	// oppgir priser — Mistral sender ingen, og da skal listeprisene stå.
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
