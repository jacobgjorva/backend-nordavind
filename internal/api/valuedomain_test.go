package api

import (
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Prod-fellen 2026-08-07: modellen filtrerte på sektor = 'Pub' — en verdi
// den aldri hadde sett — fikk null rader, og konkluderte med at bedriften
// ikke har pub-kunder. Testene under fester at koden nå plukker ut riktig
// mål for domene-oppslaget, og at vegringssvaret aldri leveres.

func testConn() dbConn {
	return dbConn{allowed: []string{"kunder", "ordrer"}, views: map[string]string{}}
}

func TestEqFilterTargetFindsColumnAndTable(t *testing.T) {
	col, table, ok := eqFilterTarget("SELECT COUNT(*) AS antall_pubber FROM kunder WHERE sektor = 'Pub';", testConn())
	if !ok || col != "sektor" || table != "kunder" {
		t.Fatalf("ventet sektor/kunder, fikk %q/%q ok=%v", col, table, ok)
	}
}

func TestEqFilterTargetStripsAliasAndQuotes(t *testing.T) {
	col, table, ok := eqFilterTarget(`SELECT * FROM "kunder" k WHERE lower(k."sektor") = 'pub'`, testConn())
	if !ok || col != "sektor" || table != "kunder" {
		t.Fatalf("alias/anførselstegn skal strippes, fikk %q/%q ok=%v", col, table, ok)
	}
}

func TestEqFilterTargetSkipsNumbersAndDates(t *testing.T) {
	if _, _, ok := eqFilterTarget("SELECT * FROM ordrer WHERE ordredato = '2024-01-01'", testConn()); ok {
		t.Error("datoer har ikke et verdidomene å ramse opp")
	}
	if _, _, ok := eqFilterTarget("SELECT * FROM ordrer WHERE ordre_id = '4412'", testConn()); ok {
		t.Error("tall har ikke et verdidomene å ramse opp")
	}
}

func TestEqFilterTargetRefusesTablesOutsideAccess(t *testing.T) {
	if _, _, ok := eqFilterTarget("SELECT * FROM hemmelig WHERE sektor = 'Pub'", testConn()); ok {
		t.Error("tabeller utenfor tilgangslista skal aldri slås opp")
	}
}

// Vegringsvakta: svaret fra prod-turen, med domenet alt servert i evidens.
func TestVegringRecheckFiresOnServedValues(t *testing.T) {
	turn := &motor.Turn{Evidence: []string{
		`Filteret traff ingen rader. Kolonnen sektor har KUN disse verdiene: "Bar", "Hotell", "Utested".`,
	}}
	draft := "Vi har ingen kunder som er merket med sektoren «Pub» i systemet. Vil du at jeg skal sjekke hvilke sektorer som faktisk finnes?"
	corr := vegringRecheck(turn, draft)
	if corr == "" {
		t.Fatal("vegring med servert domene skulle utløst ny runde")
	}
	if !strings.Contains(corr, "Kjør spørringen på nytt") {
		t.Errorf("korreksen skal be om handling, fikk %q", corr)
	}
}

// Uten servert domene er et spørsmål ikke nødvendigvis vegring — da skal
// vakta tie (fail-open, ingen formuleringsjakt på generelt grunnlag).
func TestVegringRecheckSilentWithoutServedValues(t *testing.T) {
	turn := &motor.Turn{Evidence: []string{`{"columns":["antall"],"rows":[["12"]]}`}}
	if corr := vegringRecheck(turn, "Vil du at jeg skal bryte det ned per region?"); corr != "" {
		t.Errorf("tilbud om videre arbeid er drive, ikke vegring: %q", corr)
	}
}

// Et oppklarende spørsmål om hva brukeren mener er ikke vegring.
func TestVegringRecheckIgnoresClarifyingQuestions(t *testing.T) {
	turn := &motor.Turn{Evidence: []string{`Kolonnen sektor har KUN disse verdiene: "Bar", "Hotell".`}}
	if corr := vegringRecheck(turn, "Mener du pubber i Oslo eller i hele landet?"); corr != "" {
		t.Errorf("oppklaring skal ikke felles som vegring: %q", corr)
	}
}
