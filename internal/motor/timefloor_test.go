package motor

import (
	"strings"
	"testing"
	"time"
)

var today = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

// Prod-caset ordrett: kilden sa «19 år», fødselsdatoen i samme svar beviser
// 21. Ren aritmetikk — koden retter.
func TestAgeFixCorrectsProvableAge(t *testing.T) {
	in := "Artisten Sombr (Shane Michael Boose) er 19 år gammel, født 5. juli 2005."
	out, changed := AgeFix(in, today)
	if !changed || !strings.Contains(out, "21 år") || strings.Contains(out, "19 år") {
		t.Fatalf("alderen skulle vært rettet til 21: %q", out)
	}
	// Før bursdagen: 20.
	before := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	out, _ = AgeFix(in, before)
	if !strings.Contains(out, "20 år") {
		t.Fatalf("før bursdag skal gi 20: %q", out)
	}
}

// Konservativt: to personer, to aldre — koden kan ikke bevise hvem som er
// hvem, og rører ingenting.
func TestAgeFixLeavesAmbiguousAnswersAlone(t *testing.T) {
	in := "Ola er 40 år og Kari er 35 år, født 1. mai 1990."
	if _, changed := AgeFix(in, today); changed {
		t.Fatal("flertydige svar skal ikke røres")
	}
	// Riktig alder: ingenting å rette.
	ok := "Han er 21 år, født 5. juli 2005."
	if _, changed := AgeFix(ok, today); changed {
		t.Fatal("korrekt alder skal ikke røres")
	}
}

// Frist-caset: «i år»-spørsmål besvart med kun 2024 → merknad. Historiske
// spørsmål og svar med inneværende år får ALDRI merknad.
func TestStaleNoteOnlyForDeicticQuestionsWithOldYears(t *testing.T) {
	if n := StaleNote("når er fristen i år?", "Fristen er 31. mai 2024.", today); !strings.Contains(n, "2024") || !strings.Contains(n, "2026") {
		t.Fatalf("forventet ferskhetsmerknad: %q", n)
	}
	if n := StaleNote("når ble selskapet grunnlagt?", "Selskapet ble grunnlagt i 1987.", today); n != "" {
		t.Fatalf("historisk spørsmål skal ikke merkes: %q", n)
	}
	if n := StaleNote("når er fristen i år?", "Fristen i 2026 er 31. mai.", today); n != "" {
		t.Fatalf("svar med inneværende år skal ikke merkes: %q", n)
	}
	if n := StaleNote("hva er fristen i år?", "Fristen er 31. mai hvert år.", today); n != "" {
		t.Fatalf("svar uten årstall skal ikke merkes: %q", n)
	}
}

func TestTimeDeicticWords(t *testing.T) {
	for _, q := range []string{"hvor gammel er sombr?", "hva er fristen i år?", "hva er nyeste versjon?"} {
		if !TimeDeictic(q) {
			t.Errorf("%q skulle vært tidsdeiktisk", q)
		}
	}
	for _, q := range []string{"når ble selskapet grunnlagt?", "hvem er Kari Nes?"} {
		if TimeDeictic(q) {
			t.Errorf("%q skulle IKKE vært tidsdeiktisk", q)
		}
	}
}
