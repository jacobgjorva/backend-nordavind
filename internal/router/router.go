package router

import "strings"

// Modellvalg for auto-ruting. Standardmodellen må håndtere tool-calling
// (web_search) pålitelig; kimi-modellene ignorerer reasoning-av og flash er
// for svak til verktøybruk og kildesyntese.
const (
	MidModel   = "glm-5-turbo"
	HeavyModel = "glm-5.2"
)

// heavyMarkers er signaler på at spørsmålet krever tolkning, analyse
// eller flerstegs resonnering.
var heavyMarkers = []string{
	"analyser", "analyse", "vurder", "sammenlign", "drøft", "resonner",
	"strategi", "arkitektur", "refaktor", "implementer", "debug",
	"bevis", "beregn", "regn ut", "kalkuler", "optimaliser",
	"hvorfor", "forklar hvordan", "hva er konsekvensen", "fordeler og ulemper",
	"plan for", "utred", "estimer", "risiko",
}

// Message er minimumsfeltene vi trenger fra chat-payloaden.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Pick velger modell ut fra siste brukermelding og samtalens omfang.
func Pick(messages []Message) string {
	var last string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			last = messages[i].Content
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
