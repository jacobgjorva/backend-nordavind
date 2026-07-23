package api

import (
	"strings"
	"sync"
)

// Scaleway-priser i EUR per 1M tokens [input, output]. Deres API eksponerer
// ingen pris, så vi holder tabellen selv.
var scalewayEURPerM = map[string][2]float64{
	"qwen3-235b-a22b-instruct-2507": {0.75, 2.25},
	"qwen3.5-397b-a17b":             {0.60, 3.60},
	"glm-5.2":                       {1.80, 5.50},
	"qwen3.6-35b-a3b":               {0.25, 1.50},
	"mistral-small-3.2-24b-instruct-2506": {0.15, 0.35},
}

// eurUsd omregner EUR-priser til USD, så nedstrøms USD→NOK-konvertering holder.
const eurUsd = 1.08

// pricing holder pris per token (USD) per modell.
type pricing struct {
	mu    sync.RWMutex
	perTk map[string][2]float64 // model -> [input, output] USD per token
}

func newPricing() *pricing {
	perTk := make(map[string][2]float64, len(scalewayEURPerM))
	for model, eur := range scalewayEURPerM {
		perTk[model] = [2]float64{
			eur[0] * eurUsd / 1e6,
			eur[1] * eurUsd / 1e6,
		}
	}
	return &pricing{perTk: perTk}
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
