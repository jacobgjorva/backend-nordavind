package router

import (
	"encoding/json"
	"strings"
)

// Modellvalg for auto-ruting. Standardmodellen må håndtere tool-calling
// (web_search) pålitelig og skrive korrekt norsk; kimi-modellene ignorerer
// reasoning-av, flash er for svak til verktøybruk, og glm-5-turbo lekker
// engelsk resonnering i norske svar. Mistral er også 58 % billigere.
// ÉN modell. Motor v5 er designet for Mistral Large 3 og bare den: girkasser
// mellom leverandører skjulte hvem som faktisk feilet, og hvert nivå krevde
// egen kalibrering. Vision er unntaket — bilder krever en multimodal modell.
const (
	// Chat- og arbeidsmodellen. Hos Mistral La Plateforme (EU, Frankrike).
	MidModel = "mistral-large-2512"
	// Bakoverkompatible aliaser for lagrede modellvalg i klienter.
	HeavyModel = MidModel
	TopModel   = MidModel
	LightModel = MidModel
	// VisionModel tolker bilder. UVERIFISERT NAVN: «pixtral-large-2411» ga
	// «Invalid model» mot La Plateforme, og videre testing ble stengt av
	// 401 (nøkkelen ser ut til å være scopet). Må bekreftes mot Mistrals
	// modell-liste før bildeopplasting kan brukes.
	VisionModel = "pixtral-12b-2409"
)

// Aliaser: vindskalaen navngir nivåene utad, men peker på samme modell.
var Aliases = map[string]string{
	"flau": MidModel, "bris": MidModel, "storm": MidModel,
	"orkan": MidModel, "kuling": VisionModel,
}

// katalog: modellene som FÅR gå upstream. Alt annet (utgåtte modeller fra
// gamle klienter/localStorage, tastefeil) normaliseres til MidModel — en
// deprekert modell skal aldri kunne overleve i omløp.
var katalog = map[string]bool{
	MidModel: true, VisionModel: true,
}

// Resolve oversetter et alias til faktisk modellnavn; "auto" slippes gjennom
// (løses av kompleksitetsrutingen), alle ukjente navn tvinges til MidModel.
func Resolve(model string) string {
	if real, ok := Aliases[strings.ToLower(model)]; ok {
		return real
	}
	if model == "auto" || model == "" || katalog[model] {
		return model
	}
	return MidModel
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

// Pick: én modell, så valget er trivielt. Bilder er unntaket.
func Pick(messages []Message) string {
	if LastUserHasImage(messages) {
		return VisionModel
	}
	return MidModel
}
