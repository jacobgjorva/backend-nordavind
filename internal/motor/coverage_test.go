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

// INVARIANTEN (2026-08-06): null evidens + udekkede tall i kildekrevende
// metode → utkastet leveres ALDRI; brukeren får den ærlige hukommelses-
// bunnen. Prod-hendelsen: en hel diktet tabell skipret med fotnote.
func TestDeliveryHoldsMemoryOnlyAnswer(t *testing.T) {
	out := &fakeOut{}
	v := &fakeVerifier{unsupported: []string{"100", "0,50"}}
	d := &Delivery{Out: out, Verifier: v}
	turn := &Turn{Method: MethodAdvice, Question: "hvor mye kan jeg forvente å få betalt?"}

	d.Deliver(context.Background(), turn, realMemoryAnswer)

	if len(out.content) != 1 {
		t.Fatalf("forventet ett innhold, fikk %d", len(out.content))
	}
	if strings.Contains(out.content[0], "20 og 100 kroner") {
		t.Fatalf("hukommelsestallene skulle ALDRI vært levert: %q", out.content[0])
	}
	if !strings.Contains(out.content[0], "gjetter ikke") {
		t.Errorf("den ærlige bunnen mangler: %q", out.content[0])
	}
}

// covVerifier: dømmer kun svar som inneholder det diktede tallet — så et
// vellykket omforsøk re-verifiseres som rent.
type covVerifier struct{ dirty string }

func (v covVerifier) Unsupported(answer string, _ []string) []string {
	if strings.Contains(answer, v.dirty) {
		return []string{v.dirty}
	}
	return nil
}

// Med evidens: ETT omforsøk uten de udekkede tallene; holder det, leveres det.
func TestDeliveryRegroundsWithEvidence(t *testing.T) {
	out := &fakeOut{}
	d := &Delivery{
		Out:      out,
		Verifier: covVerifier{dirty: "586"},
		Reground: func(_ context.Context, _ *Turn, _ string, _ []string) string {
			return "Mjød er størst med 514 millioner ifølge kildene."
		},
	}
	turn := &Turn{Method: MethodAnalysis, Evidence: []string{"mjød 514 millioner"}}

	d.Deliver(context.Background(), turn, "Øl er størst med 586 millioner.")

	if len(out.content) != 1 || strings.Contains(out.content[0], "586") {
		t.Fatalf("diktet tall skulle vært skrevet bort: %v", out.content)
	}
	if !strings.Contains(out.content[0], "514") {
		t.Errorf("omforsøket skulle vært levert: %q", out.content[0])
	}
}

// Omforsøk som fortsatt er udekket (eller mangler): kildefallbacken, aldri
// utkastet.
func TestDeliveryFallsBackWhenRegroundStillDirty(t *testing.T) {
	out := &fakeOut{}
	d := &Delivery{
		Out:      out,
		Verifier: covVerifier{dirty: "586"},
		Reground: func(_ context.Context, _ *Turn, _ string, _ []string) string {
			return "Fortsatt 586 millioner."
		},
	}
	turn := &Turn{Method: MethodAnalysis, Evidence: []string{"mjød 514 millioner"}}

	d.Deliver(context.Background(), turn, "Øl er størst med 586 millioner.")

	if strings.Contains(out.content[0], "586") {
		t.Fatalf("udekket tall slapp gjennom: %q", out.content[0])
	}
	if !strings.Contains(out.content[0], "holder dem") {
		t.Errorf("kildefallbacken mangler: %q", out.content[0])
	}
}

// Fail-open: klasser uten kildedeklarasjon er urørt. «En meter er 100 cm»
// i fri chat skal aldri holdes tilbake.
func TestDeliveryLeavesNonSourceMethodsAlone(t *testing.T) {
	for _, m := range []MethodKey{MethodNone, MethodSmalltalk, MethodCreative} {
		out := &fakeOut{}
		v := &fakeVerifier{unsupported: []string{"100"}}
		d := &Delivery{Out: out, Verifier: v}
		d.Deliver(context.Background(), &Turn{Method: m}, "En meter er 100 cm.")
		if len(out.content) != 1 || !strings.Contains(out.content[0], "100 cm") {
			t.Errorf("%q skal leveres urørt, fikk %v", m, out.content)
		}
	}
}

// Samtalen er gyldig grunnlag: en oppfølging som gjentar forrige turs tall
// skal IKKE holdes. Verifikatoren får samtalen i basisen.
func TestDeliveryPassesConvoToVerifier(t *testing.T) {
	v := &fakeVerifier{}
	d := &Delivery{Out: &fakeOut{}, Verifier: v, Convo: "Forrige svar: prisen er 17 000 kr per år."}
	turn := &Turn{Method: MethodAdvice, Question: "kan du oppsummere kortere?"}

	d.Deliver(context.Background(), turn, "Kort versjon: 17 000 kr per år.")

	if !strings.Contains(strings.Join(v.sawBasis, "|"), "17 000 kr per år") {
		t.Errorf("samtalen skal være del av grunnlaget: %v", v.sawBasis)
	}
}
