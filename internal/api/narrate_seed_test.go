package api

import "testing"

// Samme spørsmål stilt i ulike samtaler skal ikke åpne likt hver gang.
func TestSeedVariesOpeningLine(t *testing.T) {
	seen := map[string]bool{}
	for _, msg := range []string{
		"Hvor mye har Racamca AB kjøpt for?",
		"Hvem er største kunde?",
		"Vis de nyeste ordrene",
		"Hvor mange ordre har vi?",
		"Hva selger best?",
	} {
		n := newNarrator(func(string) {}, true, nil)
		n.seed(msg)
		seen[n.pick("a.", "b.", "c.", "d.", "e.")] = true
	}
	if len(seen) < 3 {
		t.Errorf("for lite variasjon på tvers av meldinger: %v", seen)
	}
}

// Men samme melding skal gi samme rotasjon — determinisme er kontrakten.
func TestSeedIsDeterministic(t *testing.T) {
	a := newNarrator(func(string) {}, true, nil)
	b := newNarrator(func(string) {}, true, nil)
	a.seed("Hvor mange ordre har vi?")
	b.seed("Hvor mange ordre har vi?")
	if a.turn != b.turn {
		t.Errorf("samme melding ga ulik rotasjon: %d mot %d", a.turn, b.turn)
	}
}
