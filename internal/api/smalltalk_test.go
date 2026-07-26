package api

import "testing"

func TestCannedSmalltalk(t *testing.T) {
	for _, m := range []string{"Takk!", "tusen takk", "takk for hjelpen", "Hei", "god morgen", "Takk idag", "takk i dag", "ha det bra", "supert, takk!"} {
		if cannedSmalltalk(m) == "" {
			t.Errorf("%q skulle fått ferdig svar", m)
		}
	}
	for _, m := range []string{"takk, men hva med ordrene?", "hei kan du sjekke salget", "hvor mange ordre har vi?", "Takk idag for hjelpen med det store prosjektet vårt sammen"} {
		if r := cannedSmalltalk(m); r != "" {
			t.Errorf("%q skulle IKKE fått ferdig svar, fikk %q", m, r)
		}
	}
}
