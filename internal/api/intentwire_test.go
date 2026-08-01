package api

import (
	"strings"
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

// Vedleggskroppen skal aldri nå rutingen: et UI-notat som vedlegg sendte
// «gi meg en kort oppsummering» til create_widget i prod (2026-08-01).
func TestStripAttachmentsKeepsAskDropsBody(t *testing.T) {
	in := "Gi meg en kort oppsummering takk\n\n[Vedlegg: notat.md]\nKnappen skal være grønn. Fargen på widgeten endres. Vinmonopolet-lenken flyttes."
	got := stripAttachments(in)
	if !strings.Contains(got, "Gi meg en kort oppsummering") || !strings.Contains(got, "[Vedlegg: notat.md]") {
		t.Fatalf("bestilling/filnavn mistet: %q", got)
	}
	if strings.Contains(got, "Knappen") || strings.Contains(got, "widgeten") {
		t.Fatalf("vedleggskroppen lekket: %q", got)
	}
	if s := stripAttachments("vanlig melding uten vedlegg"); s != "vanlig melding uten vedlegg" {
		t.Fatalf("melding uten vedlegg skal være urørt: %q", s)
	}
	two := stripAttachments("se på disse\n\n[Vedlegg: a.md]\ninnhold a\n\n[Vedlegg: b.md]\ninnhold b")
	if !strings.Contains(two, "a.md, b.md") || strings.Contains(two, "innhold") {
		t.Fatalf("flervedlegg feil: %q", two)
	}
}
