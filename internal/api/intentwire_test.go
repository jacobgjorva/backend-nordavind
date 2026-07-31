package api

import (
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/intent"
)

// Demoteringen er wiring-kritisk: uten den åpner «tokens» i en melding
// forbrukspanelet. Ren funksjon, full dekning over hele flyt-tabellen.
func TestDemoteExplicitOnly(t *testing.T) {
	for key, f := range intent.Flows {
		k2, f2, demoted := demoteExplicitOnly(key, f)
		if f.ExplicitOnly {
			if !demoted || k2 != intent.FreeChatKey || !f2.Knowledge {
				t.Errorf("%s: skulle demoteres til fri chat, fikk %q (demoted=%v)", key, k2, demoted)
			}
		} else if demoted || k2 != key {
			t.Errorf("%s: skulle vært urørt, fikk %q (demoted=%v)", key, k2, demoted)
		}
	}
}
