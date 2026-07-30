package motor

import (
	"context"
	"strings"
	"testing"
)

// Svaret under er ORDRETT fra prod-samtalen 2026-07-30 som avdekket
// feilklassen: en kildekrevende metode svarte med tall fra hukommelsen,
// uten ett eneste søk. Ekte modellutdata som testdata, aldri mocks.
const realMemoryAnswer = "Du kan forvente mellom 20 og 100 kroner per time, avhengig av oppgavetype " +
	"og plattform. Betalingen er nesten alltid per oppdrag, ikke per time. Enkle oppgaver som å " +
	"merke bilder eller svare på spørsmål kan gi 0,50 til 5 kroner per oppdrag, mens mer komplekse " +
	"oppgaver som transkripsjon eller kvalitetssjekk av AI-svar kan gi 10–50 kroner per oppdrag."

// Kjerneklassen: kildekrevende metode, null kilder, tall i svaret → den
// generelle hukommelses-merknaden.
func TestCoverageNoteOnMemoryOnlyAnswer(t *testing.T) {
	turn := &Turn{Method: MethodAdvice} // ingen Evidence
	note := CoverageNote(MethodAdvice, turn, []string{"100", "0,50"})
	if !strings.Contains(note, "hukommelse") {
		t.Errorf("null kilder skal gi hukommelses-merknaden, fikk %q", note)
	}
}

// Delvis dekning: noen tall udekket → navngi dem, maks tre.
func TestCoverageNoteNamesFewOffenders(t *testing.T) {
	turn := &Turn{Method: MethodAdvice, Evidence: []string{"kildetekst"}}
	note := CoverageNote(MethodAdvice, turn, []string{"137,7"})
	if !strings.Contains(note, "137,7") || !strings.Contains(note, "anslag") {
		t.Errorf("udekket tall skal navngis, fikk %q", note)
	}
	many := []string{"100", "200", "300", "400"}
	note = CoverageNote(MethodAdvice, turn, many)
	if strings.Contains(note, "400") {
		t.Errorf("over %d tall skal ikke ramses opp, fikk %q", maxNotedNumbers, note)
	}
	if !strings.Contains(note, "flere av tallene") {
		t.Errorf("mange udekkede skal gi den generelle formen, fikk %q", note)
	}
}

// Fail-open: klasser uten kildedeklarasjon får ALDRI merknad. «En meter er
// 100 cm» i fri chat skal ikke mistenkeliggjøres.
func TestCoverageNoteNeverFiresWithoutEvidenceDeclaration(t *testing.T) {
	for _, m := range []MethodKey{MethodNone, MethodSmalltalk, MethodCreative} {
		if note := CoverageNote(m, &Turn{Method: m}, []string{"100"}); note != "" {
			t.Errorf("%q deklarerer ikke kilder og skal ikke gi merknad, fikk %q", m, note)
		}
	}
}

func TestCoverageNoteSilentWithoutOffenders(t *testing.T) {
	if note := CoverageNote(MethodAdvice, &Turn{}, nil); note != "" {
		t.Errorf("uten avvik skal gulvet tie, fikk %q", note)
	}
}

// Hele kjeden gjennom leveransen, med det EKTE prod-svaret: merknaden skal
// stå i det som sendes brukeren.
func TestDeliveryAppendsCoverageNote(t *testing.T) {
	out := &fakeOut{}
	v := &fakeVerifier{unsupported: []string{"100", "0,50"}}
	d := &Delivery{Out: out, Verifier: v}
	turn := &Turn{Method: MethodAdvice, Question: "hvor mye kan jeg forvente å få betalt?"}

	d.Deliver(context.Background(), turn, realMemoryAnswer)

	if len(out.content) != 1 {
		t.Fatalf("forventet ett innhold, fikk %d", len(out.content))
	}
	if !strings.Contains(out.content[0], "Merk: dette svaret bygger på hukommelse") {
		t.Errorf("merknaden mangler i det leverte svaret: %q", out.content[0])
	}
	// Selve svaret skal stå urørt foran merknaden — aldri omskriving.
	if !strings.Contains(out.content[0], "20 og 100 kroner per time") {
		t.Error("svaret skal leveres uendret, med merknaden etter")
	}
}

// Samtalen er gyldig grunnlag: en oppfølging som gjentar forrige turs tall
// skal IKKE merkes. Verifikatoren får samtalen i basisen, så avvikslista er
// tom — testen fester at Delivery faktisk sender Convo med.
func TestDeliveryPassesConvoToVerifier(t *testing.T) {
	v := &fakeVerifier{}
	d := &Delivery{Out: &fakeOut{}, Verifier: v, Convo: "Forrige svar: prisen er 17 000 kr per år."}
	turn := &Turn{Method: MethodAdvice, Question: "kan du oppsummere kortere?"}

	d.Deliver(context.Background(), turn, "Kort versjon: 17 000 kr per år.")

	if !strings.Contains(strings.Join(v.sawBasis, "|"), "17 000 kr per år") {
		t.Errorf("samtalen skal være del av grunnlaget: %v", v.sawBasis)
	}
}
