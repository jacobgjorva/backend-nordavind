package api

import (
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/intent"
)

// REGEL (Jacob 2026-07-26): e-post sendes KUN ved at brukeren klikker Send i
// kortet — aldri av modellen i chatten. Ingen chat-flyt får derfor ha et
// verktøy som faktisk sender; mail_compose (rendrer kortet) er eneste utvei.
func TestNoAutonomousMailSendInChatFlows(t *testing.T) {
	for key, f := range intent.Flows {
		for _, tool := range f.Tools {
			if tool == "send_mail" || tool == "mail_send" {
				t.Errorf("flyt %s har sendeverktøy %q — sending skal kun skje via brukerklikk", key, tool)
			}
		}
	}
	// Verktøyet som finnes skal være compose-kortet, ikke sending.
	fn := mailComposeTool["function"].(map[string]any)
	if fn["name"] != "mail_compose" {
		t.Fatalf("uventet mail-verktøy: %v", fn["name"])
	}
}
