package api

import "testing"

// Å slette tools mens historikken har tool_calls gir HTTP 400 fra upstream —
// det gjorde backstoppen død på nettopp de søketunge turene den finnes for.
func TestDisableToolsKeepsCatalogAfterToolUse(t *testing.T) {
	full := map[string]any{
		"tools": []any{webSearchTool},
		"messages": []any{
			map[string]any{"role": "user", "content": "spørsmål"},
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "toolcallaaa", "type": "function"},
			}},
			map[string]any{"role": "tool", "tool_call_id": "toolcallaaa", "content": "resultat"},
		},
	}
	disableTools(full)
	if _, ok := full["tools"]; !ok {
		t.Fatal("katalogen ble slettet selv om historikken har tool_calls — gir 400")
	}
	if full["tool_choice"] != "none" {
		t.Fatalf("tool_choice = %v, vil none", full["tool_choice"])
	}
}

// Uten verktøybruk kan katalogen fjernes — det sparer ~3k tokens.
func TestDisableToolsDropsCatalogWhenUnused(t *testing.T) {
	full := map[string]any{
		"tools": []any{webSearchTool},
		"messages": []any{
			map[string]any{"role": "user", "content": "hei"},
			map[string]any{"role": "assistant", "content": "hei!"},
		},
	}
	disableTools(full)
	if _, ok := full["tools"]; ok {
		t.Fatal("katalogen skulle vært fjernet når ingen verktøy er brukt")
	}
	if full["tool_choice"] != "none" {
		t.Fatal("tool_choice skal være none")
	}
}
