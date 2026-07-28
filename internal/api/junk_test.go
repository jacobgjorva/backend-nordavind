package api

import "testing"

// De to målte lekkasjene, pluss grensene som IKKE skal treffe.
func TestIsJunkAnswer(t *testing.T) {
	junk := []string{
		`ermannquery": "Brent Crude monthly average price 2025 USD per barrel"}`,
		"…………」",
		"eskola",
		"lium",
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

// Det målte tilfellet: arabisk søppelhode foran gyldig norsk setning.
func TestStripForeignHead(t *testing.T) {
	cases := map[string]string{
		"جيكا Det finnes ingen åpent tilgjengelige priser.": "Det finnes ingen åpent tilgjengelige priser.",
		"нымі Salget steg i 2025.":                          "Salget steg i 2025.",
		// Skal IKKE røres:
		"Det finnes ingen priser.": "Det finnes ingen priser.",
		"Brent-prisen falt.":       "Brent-prisen falt.",
		"426 ordre.":               "426 ordre.",
		"جيكا كل شيء باللغة العربية هنا": "جيكا كل شيء باللغة العربية هنا",
	}
	for in, want := range cases {
		if got := stripForeignHead(in); got != want {
			t.Errorf("stripForeignHead(%q) = %q, ventet %q", in, got, want)
		}
	}
}

// Vaktmønsteret dekker de målte kapitulasjonene fra prod.
func TestImpossibleReCoversMeasuredPhrases(t *testing.T) {
	for _, s := range []string{
		"Uten disse tallene kan jeg ikke regne ut korrelasjonen med salget.",
		"Da kan vi ikke sammenligne periodene direkte.",
		"Det finnes ingen åpent tilgjengelige, fullstendige daglige Brent-priser.",
	} {
		if !impossibleRe.MatchString(s) {
			t.Errorf("kapitulasjon ikke gjenkjent: %q", s)
		}
	}
	if impossibleRe.MatchString("Jeg regner ut korrelasjonen nå.") {
		t.Error("normal handling feiltolket")
	}
}
