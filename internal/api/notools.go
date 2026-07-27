package api

// disableTools slår av verktøybruk for et syntese-kall (siste runde, backstop,
// regrounding) — men bare på en måte upstream faktisk godtar.
//
// BUGGEN dette retter: å SLETTE "tools" mens meldingene inneholder tool_calls
// gir HTTP 400 fra Mistral («validation error»), fordi et verktøykall i
// historikken må kunne slås opp i katalogen. Kallet feilet dermed alltid, og
// backstoppen — vår siste redning mot tomme svar — svarte «Klarte ikke hente
// et svar nå» på nettopp de søketunge turene den fantes for. Målt: samme
// payload med tools beholdt svarer perfekt.
//
// Regelen er derfor: har turen brukt verktøy, BEHOLD katalogen og la
// tool_choice="none" gjøre jobben. Har den ikke det, kan katalogen fjernes og
// vi sparer ~3k tokens.
func disableTools(full map[string]any) {
	full["tool_choice"] = "none"
	if !hasToolCalls(full) {
		delete(full, "tools")
	}
}

// hasToolCalls: inneholder meldingshistorikken et verktøykall eller et
// verktøysvar? Da må katalogen bli stående.
func hasToolCalls(full map[string]any) bool {
	msgs, _ := full["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if mm["role"] == "tool" {
			return true
		}
		if tc, ok := mm["tool_calls"]; ok && tc != nil {
			if arr, ok := tc.([]any); !ok || len(arr) > 0 {
				return true
			}
		}
	}
	return false
}
