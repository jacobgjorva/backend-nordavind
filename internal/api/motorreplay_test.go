package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Replay: EKTE modellsvar fra loop-kjøringene, med den ordrette kildeteksten
// verktøyene returnerte, kjørt gjennom tallkontrollen.
//
// Dette er ikke mocks. Fixturen er innspilt virkelighet — nøyaktig det
// modellen skrev og nøyaktig det den hadde å skrive fra. Uten den kan vi
// bare teste kontrollen mot tall vi har funnet på selv, og da måler vi vår
// egen fantasi.
//
// Regenerer fra probe_runs/loop_runs når nye kjøringer finnes.

type replayCase struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Evidence []string `json:"evidence"`
}

func loadReplay(t *testing.T) []replayCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/motor-replay.json")
	if err != nil {
		t.Fatalf("les replay-fixtur: %v", err)
	}
	var cases []replayCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse replay-fixtur: %v", err)
	}
	if len(cases) < 8 {
		t.Fatalf("fixturen er for tynn: %d caser", len(cases))
	}
	return cases
}

// Tallkontrollen skal ikke rope ulv på ekte, godt forankrede svar. Falske
// positive er dyrere enn falske negative: hver gang kontrollen felte et
// korrekt svar, fikk brukeren en naken tabell i stedet for prosa.
func TestVerifierIsQuietOnRealGroundedAnswers(t *testing.T) {
	v := motorVerifier{}
	noisy := 0
	for _, c := range loadReplay(t) {
		basis := append(append([]string{}, c.Evidence...), c.Question)
		off := v.Unsupported(c.Answer, basis)
		if len(off) > 0 {
			noisy++
			t.Logf("%s: %d avvik: %v", c.ID, len(off), off)
		}
	}
	// Disse svarene ble manuelt kontrollert mot kildene (probeloggen,
	// kjøring 7): alle navn og tall var dekket. Slår kontrollen ut på mer
	// enn ett av ti, er den for streng til å brukes som telemetri.
	if noisy > 1 {
		t.Errorf("tallkontrollen slo ut på %d av 10 forankrede svar — for mange falske positive", noisy)
	}
}

// Kontrollen må likevel FANGE et tall som ikke finnes i kildene, ellers er
// telemetrien verdiløs. Vi tar et ekte svar og bytter ut ett tall.
func TestVerifierCatchesInventedNumber(t *testing.T) {
	v := motorVerifier{}
	cases := loadReplay(t)

	var probe replayCase
	for _, c := range cases {
		if strings.Contains(c.Answer, "2 990") {
			probe = c
			break
		}
	}
	if probe.ID == "" {
		t.Skip("fixturen har ingen case med et kjent kildetall")
	}

	invented := strings.Replace(probe.Answer, "2 990", "7 431", 1)
	basis := append(append([]string{}, probe.Evidence...), probe.Question)
	off := v.Unsupported(invented, basis)

	found := false
	for _, o := range off {
		if normDigits(o) == "7431" {
			found = true
		}
	}
	if !found {
		t.Errorf("et oppdiktet tall skal fanges, fikk %v", off)
	}
}

// Brukerens eget tall er aldri dikting. Sier brukeren «vi omsetter for 40
// millioner», skal svaret kunne gjenta det uten å bli logget.
func TestVerifierAcceptsNumbersFromTheQuestion(t *testing.T) {
	v := motorVerifier{}
	question := "Selskapet vårt omsetter for 40 millioner. Hvem ligner på oss?"
	answer := "Med 40 millioner i omsetning ligner dere mest på de mellomstore aktørene."

	if off := v.Unsupported(answer, []string{"kildetekst uten tall", question}); len(off) > 0 {
		t.Errorf("brukerens eget tall skal ikke være avvik, fikk %v", off)
	}
}

// Uthevingen modellen faktisk produserer skal forsvinne fra ekte svar, og
// alt innhold skal overleve.
func TestStripEmphasisOnRealAnswers(t *testing.T) {
	for _, c := range loadReplay(t) {
		got := motor.StripEmphasis(c.Answer)
		if strings.Contains(got, "**") {
			t.Errorf("%s: fet skrift overlevde: %q", c.ID, got[:min(120, len(got))])
		}
		// Ingen tekst skal gå tapt utover markørene selv.
		want := len([]rune(strings.ReplaceAll(strings.ReplaceAll(c.Answer, "**", ""), "__", "")))
		if diff := want - len([]rune(got)); diff > 12 {
			t.Errorf("%s: %d tegn forsvant utover markørene", c.ID, diff)
		}
	}
}
