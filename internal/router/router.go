package router

import "strings"

// Modellvalg for auto-ruting. Standardmodellen må håndtere tool-calling
// (web_search) pålitelig og skrive korrekt norsk; kimi-modellene ignorerer
// reasoning-av, flash er for svak til verktøybruk, og glm-5-turbo lekker
// engelsk resonnering i norske svar. Mistral er også 58 % billigere.
const (
	MidModel   = "qwen3-235b-a22b-instruct-2507"
	HeavyModel = "glm-5.2"
	// VisionModel tolker bilder — Qwen3.6-35B er multimodal og rimelig.
	VisionModel = "qwen3.6-35b-a3b"
)

// Nordavind-aliaser: vindskalaen navngir modellnivåene utad.
var Aliases = map[string]string{
	"bris":  MidModel,
	"storm": HeavyModel,
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
// eller flerstegs resonnering.
var heavyMarkers = []string{
	"analyser", "analyse", "vurder", "sammenlign", "drøft", "resonner",
	"strategi", "arkitektur", "refaktor", "implementer", "debug",
	"bevis", "beregn", "regn ut", "kalkuler", "optimaliser",
	"hvorfor", "forklar hvordan", "hva er konsekvensen", "fordeler og ulemper",
	"plan for", "utred", "estimer", "risiko",
}

// Message er minimumsfeltene vi trenger fra chat-payloaden. Content kan være
// ren tekst eller multimodale deler (tekst + image_url) — derfor any.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// contentText henter tekstdelene av en melding (string eller parts-array).
func contentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
					b.WriteString(" ")
				}
			}
		}
		return b.String()
	}
	return ""
}

// hasImage er sann hvis noen melding inneholder en bilde-del.
func hasImage(messages []Message) bool {
	for _, m := range messages {
		parts, ok := m.Content.([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			if mp, ok := p.(map[string]any); ok && mp["type"] == "image_url" {
				return true
			}
		}
	}
	return false
}

// Pick velger modell ut fra siste brukermelding og samtalens omfang.
func Pick(messages []Message) string {
	// Bilder krever multimodal modell uansett kompleksitet.
	if hasImage(messages) {
		return VisionModel
	}
	var last string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			last = contentText(messages[i].Content)
			break
		}
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
	lower := strings.ToLower(text)
	for _, marker := range heavyMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
