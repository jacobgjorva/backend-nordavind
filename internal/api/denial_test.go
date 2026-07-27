package api

import (
	"strings"
	"testing"
)

func TestProperNounsFindsCompanyName(t *testing.T) {
	got := properNouns("Hvor mye har Racamca AB kjøpt for?")
	if len(got) == 0 || !strings.Contains(strings.Join(got, "|"), "Racamca AB") {
		t.Fatalf("fant ikke navnet, fikk %v", got)
	}
}

// Første ord i meldingen er stort uansett og er ikke en navnekandidat.
func TestProperNounsIgnoresSentenceStart(t *testing.T) {
	for _, n := range properNouns("Hvor mange ordre har vi?") {
		if n == "Hvor" {
			t.Errorf("første ord ble tolket som navn: %v", n)
		}
	}
}

func TestProperNounsSkipsShortWords(t *testing.T) {
	for _, n := range properNouns("gjelder det AB og NO?") {
		if len([]rune(n)) < 4 {
			t.Errorf("for kort kandidat slapp gjennom: %q", n)
		}
	}
}

func TestDeniedRePattern(t *testing.T) {
	deny := []string{
		"Racamca AB finnes ikke i databasen.",
		"Racamca AB har ikke registrert kjøp i databasen.",
		"Racamca AB har ikke forekommet i ordredataene.",
		"Ingen treff på det navnet.",
	}
	for _, d := range deny {
		if !deniedRe.MatchString(d) {
			t.Errorf("avvisning ikke gjenkjent: %q", d)
		}
	}
	keep := []string{
		"Brüdog Bar Malmö AB er den viktigste kunden.",
		"Det er totalt 17 682 ordrer.",
	}
	for _, k := range keep {
		if deniedRe.MatchString(k) {
			t.Errorf("vanlig svar ble tolket som avvisning: %q", k)
		}
	}
}
