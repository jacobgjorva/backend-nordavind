package api

import (
	"encoding/json"
	"os"
	"strings"
)

// Kontekst-regnskap: hvor tokenene FAKTISK går i en tur.
//
// Uten dette er optimalisering gjetting. Slås på med PAYLOAD_SHAPE=on og er
// helt av ellers — dette skal aldri koste noe i drift.
var payloadShapeOn = os.Getenv("PAYLOAD_SHAPE") == "on"

func (s *Server) logPayloadShape(round int, full map[string]any, total int) {
	if !payloadShapeOn {
		return
	}
	msgs, _ := full["messages"].([]any)
	var system, user, assistant, tool int
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		n := len(contentString(mm["content"]))
		// Verktøykall bærer argumentene sine utenom content.
		if tc, ok := mm["tool_calls"]; ok {
			raw, _ := json.Marshal(tc)
			n += len(raw)
		}
		switch mm["role"] {
		case "system":
			system += n
		case "user":
			user += n
		case "assistant":
			assistant += n
		case "tool":
			tool += n
		}
	}
	// Tell TEGN, ikke JSON: Go escaper æøå som \uXXXX, og da ville regnskapet
	// vist seks tegn der modellen ser ett token-fragment.
	toolChars := 0
	if list, ok := full["tools"].([]any); ok {
		for _, t := range list {
			toolChars += len([]rune(flatten(t)))
		}
	}
	toolsRaw := []byte(strings.Repeat("x", toolChars))
	if round == 0 && system > 0 {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
				txt := contentString(mm["content"])
				for i, del := range strings.Split(txt, "\n\n") {
					head := del
					if len([]rune(head)) > 60 {
						head = string([]rune(head)[:60])
					}
					s.log.Info("system-del", "nr", i, "tegn", len([]rune(del)),
						"start", strings.ReplaceAll(head, "\n", " "))
				}
			}
		}
	}
	s.log.Info("kontekst-regnskap",
		"runde", round,
		"totalt_tegn", total,
		"system", system,
		"bruker", user,
		"assistent", assistant,
		"verktøysvar", tool,
		"verktøyskjema", len(toolsRaw),
		"meldinger", len(msgs))
}

// flatten trekker ut all tekst fra en verktøydefinisjon.
func flatten(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		var b strings.Builder
		for k, x := range t {
			b.WriteString(k)
			b.WriteString(flatten(x))
		}
		return b.String()
	case []any:
		var b strings.Builder
		for _, x := range t {
			b.WriteString(flatten(x))
		}
		return b.String()
	}
	return ""
}

// contentString håndterer både ren tekst og deler (tekst + bilde).
func contentString(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}
