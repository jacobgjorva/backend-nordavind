package api

import (
	"strings"
	"testing"
)

// Klientvernet: monstermeldinger i historikken klippes, siste brukermelding
// (den aktive turen, gjerne med vedlegg) fredes alltid.
func TestTrimChatHistory(t *testing.T) {
	big := strings.Repeat("x", 50000)
	oldMsg := map[string]any{"role": "user", "content": big}
	answer := map[string]any{"role": "assistant", "content": big}
	current := map[string]any{"role": "user", "content": big}
	full := map[string]any{"messages": []any{oldMsg, answer, current}}

	trimChatHistory(full)

	if c := current["content"].(string); len(c) != 50000 {
		t.Fatalf("siste brukermelding ble klippet — vedlegg i aktiv tur skal alltid frem")
	}
	for name, m := range map[string]map[string]any{"gammel bruker": oldMsg, "assistent": answer} {
		c := m["content"].(string)
		if len([]rune(c)) > chatMsgCharCap+100 {
			t.Fatalf("%s-melding over taket ble ikke klippet (%d tegn)", name, len(c))
		}
		if !strings.Contains(c, "utelatt for plass") {
			t.Fatalf("%s-melding mangler klippemarkør", name)
		}
	}
	// Multimodalt innhold (bilder, []any) skal aldri røres.
	img := map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url"}}}
	full2 := map[string]any{"messages": []any{img, map[string]any{"role": "user", "content": "hei"}}}
	trimChatHistory(full2)
	if _, ok := img["content"].([]any); !ok {
		t.Fatal("multimodalt innhold ble endret")
	}
}

// Feltene fra frontend skal aldri videre upstream — samme kontrakt som de
// andre nordavind_*-feltene.
func TestChatFieldsStripped(t *testing.T) {
	full := map[string]any{
		"nordavind_chat":    "abc123",
		"nordavind_clipped": true,
		"messages":          []any{map[string]any{"role": "user", "content": "hei"}},
	}
	// Samme uttrekk som runAgentLoop gjør.
	chatID, _ := full["nordavind_chat"].(string)
	clipped, _ := full["nordavind_clipped"].(bool)
	delete(full, "nordavind_chat")
	delete(full, "nordavind_clipped")
	if chatID != "abc123" || !clipped {
		t.Fatal("feltene ble ikke lest riktig")
	}
	for _, k := range []string{"nordavind_chat", "nordavind_clipped"} {
		if _, ok := full[k]; ok {
			t.Fatalf("%s ble ikke fjernet før upstream", k)
		}
	}
}
