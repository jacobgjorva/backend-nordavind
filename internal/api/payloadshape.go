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
	toolsRaw, _ := json.Marshal(full["tools"])
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
