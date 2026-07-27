package api

import "testing"

// De to målte lekkasjene, pluss grensene som IKKE skal treffe.
func TestIsJunkAnswer(t *testing.T) {
	junk := []string{
		`ermannquery": "Brent Crude monthly average price 2025 USD per barrel"}`,
		"…………」",
		`{"connection_id": "abc", "sql": "SELECT 1"}`,
	}
	for _, s := range junk {
		if !isJunkAnswer(s) {
			t.Errorf("søppel slapp gjennom: %q", s)
		}
	}
	fine := []string{
		"Det er ingen tydelig korrelasjon mellom oljepris og salg i 2025.",
		"426 ordre.",
		"Salget steg 12 %, mens oljeprisen falt.",
		// Tabellblokk: legitim JSON inne i fence skal ALDRI regnes som søppel.
		"Her er tallene:\n```table\n{\"columns\":[\"a\"],\"rows\":[[\"1\"]]}\n```",
	}
	for _, s := range fine {
		if isJunkAnswer(s) {
			t.Errorf("gyldig svar flagget som søppel: %q", s)
		}
	}
}

// Kapitulasjonsvaktens utløser: målte formuleringer treffer, konklusjoner gjør ikke.
func TestImpossibleRe(t *testing.T) {
	hits := []string{
		"Det er ikke mulig å fastslå en klar korrelasjon uten daglige tall.",
		"Korrelasjonsanalysen kan ikke gjennomføres.",
		"Det finnes ingen tilgjengelige daglige oljeprisdata for 2025 ennå.",
	}
	for _, s := range hits {
		if !impossibleRe.MatchString(s) {
			t.Errorf("kapitulasjon ikke gjenkjent: %q", s)
		}
	}
	misses := []string{
		"Det er ingen tydelig korrelasjon mellom oljepris og salg.",
		"Det finnes ingen direkte korrelasjon i tallene.",
		"Salget steg mens oljeprisen falt.",
	}
	for _, s := range misses {
		if impossibleRe.MatchString(s) {
			t.Errorf("legitim konklusjon feiltolket som kapitulasjon: %q", s)
		}
	}
}
