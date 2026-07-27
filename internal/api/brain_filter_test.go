package api

import "testing"

// Filteret foran uttrekket er ren tekstbehandling og avgjør hva hjernen
// koster i drift: hver melding som slipper gjennom er ett modellkall.
func TestWorthLearning(t *testing.T) {
	skal := []string{
		"Ola Berg er ansvarlig for kunden Vestland Fisk",
		"Vestland Fisk krever helsesertifikat 48 timer før levering",
		"Kari Nes godkjenner alle ordrer over 200 000 kroner",
		"Husk at vi alltid sender faktura første virkedag i måneden",
	}
	for _, q := range skal {
		if !worthLearning(q) {
			t.Errorf("skulle vært lært: %q", q)
		}
	}
	skalIkke := []string{
		"Hvem er sjefen min?",
		"Hva er forskjellen på SQLite og Postgres?",
		"Når har vi ukeplanleggingsmøte?",
		"Kan du lage en graf over omsetningen i år?",
		"takk",
		"Vis meg de siste ordrene fra i går",
	}
	for _, q := range skalIkke {
		if worthLearning(q) {
			t.Errorf("skulle vært hoppet over: %q", q)
		}
	}
}

// Et spørsmål som OGSÅ bærer en påstand skal fortsatt læres.
func TestWorthLearningKeepsStatementsInsideQuestions(t *testing.T) {
	q := "Ola Berg er ansvarlig for Vestland Fisk, når møter jeg ham?"
	if !worthLearning(q) {
		t.Errorf("påstanden i spørsmålet gikk tapt: %q", q)
	}
}
