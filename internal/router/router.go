package router

import (
	"encoding/json"
	"strings"
)

// Modellvalg for auto-ruting. Standardmodellen må håndtere tool-calling
// (web_search) pålitelig og skrive korrekt norsk; kimi-modellene ignorerer
// reasoning-av, flash er for svak til verktøybruk, og glm-5-turbo lekker
// engelsk resonnering i norske svar. Mistral er også 58 % billigere.
const (
	// Modellene kjøres på Scaleway (EU-eid, GDPR). Tre tekstnivåer:
	// Bris (hverdag), Storm (tungt), Tornado (det aller tyngste).
	MidModel   = "mistral-medium-3.5-128b"
	HeavyModel = "qwen3.5-397b-a17b"
	TopModel   = "glm-5.2"
	// VisionModel tolker bilder — Qwen3.6-35B er multimodal og rimelig.
	VisionModel = "qwen3.6-35b-a3b"
)

// Nordavind-aliaser: vindskalaen navngir modellnivåene utad.
var Aliases = map[string]string{
	"bris":   MidModel,
	"storm":  HeavyModel,
	"orkan":  TopModel,
	"kuling": VisionModel,
}

// Resolve oversetter et alias til faktisk modellnavn; ukjente navn
// returneres uendret.
func Resolve(model string) string {
	if real, ok := Aliases[strings.ToLower(model)]; ok {
		return real
	}
	return model
}

// heavyMarkers er signaler på at spørsmålet krever tolkning, analyse
// eller flerstegs resonnering (Storm).
var heavyMarkers = []string{
	"analyser", "analyse", "vurder", "sammenlign", "drøft", "resonner",
	"strategi", "arkitektur", "refaktor", "implementer", "debug",
	"bevis", "beregn", "regn ut", "kalkuler", "optimaliser",
	"hvorfor", "forklar hvordan", "hva er konsekvensen", "fordeler og ulemper",
	"plan for", "utred", "estimer", "risiko",
}

// topMarkers signaliserer de aller tyngste oppgavene (Tornado) — dyp,
// flerstegs resonnering der Storm ikke strekker til.
var topMarkers = []string{
	"bevis", "utled", "optimaliser", "matematisk", "kompleks", "dypdykk",
	"grundig analyse", "detaljert plan", "trinn for trinn", "steg for steg",
	"omfattende", "avansert", "algoritme",
}

// Message er minimumsfeltene vi trenger fra chat-payloaden. Content er rå
// JSON fordi multimodale meldinger sender en array av deler (tekst + bilde)
// i stedet for en enkel streng.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// text henter ut ren tekst fra en melding, uansett om content er en streng
// eller en array av OpenAI-innholdsdeler.
func (m Message) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
				b.WriteString(" ")
			}
		}
		return b.String()
	}
	return ""
}

// hasImage er sann hvis meldingen inneholder en bilde-del.
func (m Message) hasImage() bool {
	if len(m.Content) == 0 || m.Content[0] != '[' {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}

// LastUserHasImage er sann hvis den siste brukermeldingen inneholder et
// bilde. Vi ruter til vision-modellen kun for selve bildemeldingen —
// oppfølgingsspørsmål uten nytt bilde går tilbake til Bris/Storm, som svarer
// ut fra vision-modellens beskrivelse i samtalen.
func LastUserHasImage(messages []Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].hasImage()
		}
	}
	return false
}

// Pick velger modell ut fra siste brukermelding og samtalens omfang.
func Pick(messages []Message) string {
	var last string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			last = messages[i].text()
			break
		}
	}
	if isTop(last) {
		return TopModel
	}
	if isHeavy(last) {
		return HeavyModel
	}
	return MidModel
}

func isHeavy(text string) bool {
	// Lange spørsmål bærer som regel mer kontekst og krever mer av modellen.
	if len(text) > 600 {
		return true
	}
	return matchesAny(text, heavyMarkers)
}

func isTop(text string) bool {
	// Svært lange eller eksplisitt tunge oppgaver går til Tornado.
	if len(text) > 1600 {
		return true
	}
	return matchesAny(text, topMarkers)
}

func matchesAny(text string, markers []string) bool {
	lower := strings.ToLower(text)
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
