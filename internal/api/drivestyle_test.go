package api

import "testing"

func TestStripGenericCloserCutsFiller(t *testing.T) {
	in := "Omsetningen i juli ble 412 000 kr. Si gjerne ifra om du lurer på noe mer!"
	want := "Omsetningen i juli ble 412 000 kr."
	if got := stripGenericCloser(in); got != want {
		t.Fatalf("fikk %q", got)
	}
}

func TestStripGenericCloserCutsStackedFiller(t *testing.T) {
	in := "Rapporten er klar. Håper det hjelper! Ikke nøl med å ta kontakt."
	want := "Rapporten er klar."
	if got := stripGenericCloser(in); got != want {
		t.Fatalf("fikk %q", got)
	}
}

func TestStripGenericCloserKeepsRealOffers(t *testing.T) {
	in := "Marginen er 12 % under normalen. Vil du at jeg bryter det ned per kunde?"
	if got := stripGenericCloser(in); got != in {
		t.Fatalf("ekte tilbud skal stå, fikk %q", got)
	}
}

func TestStripGenericCloserKeepsAssessments(t *testing.T) {
	in := "Tallene er stabile. Det ser sunt ut, men følg med på fraktkostnadene."
	if got := stripGenericCloser(in); got != in {
		t.Fatalf("vurderinger skal stå, fikk %q", got)
	}
}

func TestStripGenericCloserNeverEmptiesAnswer(t *testing.T) {
	in := "Håper det hjelper!"
	if got := stripGenericCloser(in); got != in {
		t.Fatalf("rent fyll-svar beholdes urørt, fikk %q", got)
	}
}
