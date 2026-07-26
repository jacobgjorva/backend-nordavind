package api

import "testing"

// Notis-gaten: eksplisitte «husk …»-utsagn skal alltid gjennom, korte
// spørsmål og småprat aldri.
func TestWorthExtractingNotes(t *testing.T) {
	for _, q := range []string{
		"husk: purring etter 14 dager",
		"noter at Marie tar tollkoder",
		"Merk deg at lageret telles siste fredag i kvartalet",
	} {
		if !worthExtracting(q) {
			t.Errorf("notis skulle vært verdt uttrekk: %q", q)
		}
	}
	for _, q := range []string{"hei!", "hva er klokka?", "husk å le"} {
		if worthExtracting(q) {
			t.Errorf("skulle IKKE utløst uttrekk: %q", q)
		}
	}
}
