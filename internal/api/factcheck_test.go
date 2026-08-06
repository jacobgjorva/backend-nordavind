package api

import "testing"

// Målt i drive-eval 2026-08-06: dommeren underkjente RÅD og premisser
// («minstesum på 500 kroner mangler belegg») selv om de flaggede tallene var
// greie beregninger — svaret falt til naken tabell. Mandatet håndheves nå i
// kode: bare dommer som peker på en faktisk flagget verdi teller.

func TestFactVerdictsIgnoreAdviceNotTiedToOffender(t *testing.T) {
	body := `{"dommer":[
		{"verdi":"0,15","ok":true},
		{"verdi":"anbefalingen om minstesum på 500 kroner","ok":false,"grunn":"konklusjon uten belegg i grunnlaget"}
	]}`
	ok, problems := parseFactVerdicts(body, []string{"0,15"})
	if !ok || len(problems) != 0 {
		t.Fatalf("råd uten flagget verdi skal ikke underkjenne svaret, fikk ok=%v %v", ok, problems)
	}
}

func TestFactVerdictsStillCatchFabricatedNumber(t *testing.T) {
	body := `{"dommer":[{"verdi":"10 %","ok":false,"grunn":"det finnes ingen tall for 2024 i grunnlaget"}]}`
	ok, problems := parseFactVerdicts(body, []string{"10", "340"})
	if ok || len(problems) != 1 {
		t.Fatalf("diktet tall skal fortsatt stanse svaret, fikk ok=%v %v", ok, problems)
	}
}

func TestFactVerdictsMatchNumbersAcrossFormatting(t *testing.T) {
	body := `{"dommer":[{"verdi":"453000","ok":false,"grunn":"finnes ikke"}]}`
	if ok, _ := parseFactVerdicts(body, []string{"453 000"}); ok {
		t.Fatal("«453 000» og «453000» er samme verdi — dommen skal telle")
	}
}

func TestFactVerdictsAllApprovedPasses(t *testing.T) {
	body := `{"dommer":[{"verdi":"148","ok":true},{"verdi":"625 000","ok":true}]}`
	if ok, problems := parseFactVerdicts(body, []string{"148", "625 000"}); !ok || len(problems) > 0 {
		t.Fatalf("godkjente beregninger skal passere, fikk ok=%v %v", ok, problems)
	}
}

func TestFactVerdictsLegacyFormatFiltered(t *testing.T) {
	// Gammelt svarformat: problemer uten kobling til flagget verdi forkastes.
	body := `{"ok":false,"problemer":["anbefalingen mangler belegg"]}`
	if ok, _ := parseFactVerdicts(body, []string{"0,15"}); !ok {
		t.Fatal("legacy-problem uten flagget verdi skulle vært forkastet")
	}
	body = `{"ok":false,"problemer":["tallet 10 % har ingen dekning"]}`
	if ok, _ := parseFactVerdicts(body, []string{"10"}); ok {
		t.Fatal("legacy-problem om en flagget verdi skal fortsatt stanse svaret")
	}
}

func TestFactVerdictsUnparsableFailsOpen(t *testing.T) {
	if ok, _ := parseFactVerdicts(`{ikke json`, []string{"1"}); !ok {
		t.Fatal("uparsbar dom skal slippe gjennom (fail-open)")
	}
	if ok, _ := parseFactVerdicts(`{"dommer":[]}`, []string{"1"}); !ok {
		t.Fatal("tom dom skal slippe gjennom")
	}
}
