package api

import (
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

func newFloors(question string, state *motorTurnState, emit func(string)) *motorFloors {
	return &motorFloors{
		s: quietServer(), emit: emit, narr: newNarrator(emit, true, nil),
		state: state, question: question,
	}
}

// Tabellgarantien utløses av BESTILLINGEN og av at rader finnes — aldri av
// hvordan svaret er formulert.
func TestTableFloorTriggersOnRequestAndRows(t *testing.T) {
	table := `{"columns":["kunde"],"rows":[["Valhall"]]}`

	var sent []string
	f := newFloors("vis de ti største kundene i en tabell",
		&motorTurnState{lastTable: table}, func(s string) { sent = append(sent, s) })
	f.Table(&motor.Turn{})
	if len(sent) != 1 || !strings.Contains(sent[0], "```table") {
		t.Errorf("tabellen skulle vært vist: %v", sent)
	}
	if !f.state.tableShown {
		t.Error("visningen skal merkes")
	}

	// Allerede vist: aldri to ganger.
	f.Table(&motor.Turn{})
	if len(sent) != 1 {
		t.Errorf("tabellen ble vist to ganger: %v", sent)
	}

	// Ingen tabellbestilling: da er tabellen ikke svaret.
	sent = nil
	f2 := newFloors("hva var omsetningen i juni?",
		&motorTurnState{lastTable: table}, func(s string) { sent = append(sent, s) })
	f2.Table(&motor.Turn{})
	if len(sent) != 0 {
		t.Errorf("uten bestilling skal tabellen ikke vises: %v", sent)
	}

	// Ingen rader: ingenting å vise.
	sent = nil
	f3 := newFloors("vis i en tabell", &motorTurnState{}, func(s string) { sent = append(sent, s) })
	f3.Table(&motor.Turn{})
	if len(sent) != 0 {
		t.Errorf("uten rader skal ingenting vises: %v", sent)
	}
}

// Kildenoten gjelder bare når tenanten faktisk har flere kilder OG
// spørringen lyktes.
func TestSourceNoteOnlyWhenMultipleSourcesAndSuccess(t *testing.T) {
	att := dbAttempt{outcome: dbOK, conn: "Mjødhallen ERP"}

	var sent []string
	f := newFloors("q", &motorTurnState{dbOK: true, attempts: []dbAttempt{att}},
		func(s string) { sent = append(sent, s) })
	f.multiSource = true
	f.SourceNote(&motor.Turn{}, "Omsetningen var 12 millioner.")
	if len(sent) != 1 || !strings.Contains(sent[0], "Mjødhallen ERP") {
		t.Errorf("kilden skulle vært navngitt: %v", sent)
	}

	// Én kilde: noten er støy.
	sent = nil
	f2 := newFloors("q", &motorTurnState{dbOK: true, attempts: []dbAttempt{att}},
		func(s string) { sent = append(sent, s) })
	f2.multiSource = false
	f2.SourceNote(&motor.Turn{}, "svar")
	if len(sent) != 0 {
		t.Errorf("med én kilde skal noten utebli: %v", sent)
	}

	// Feilet spørring: ingen kilde å navngi.
	sent = nil
	f3 := newFloors("q", &motorTurnState{dbTried: true}, func(s string) { sent = append(sent, s) })
	f3.multiSource = true
	f3.SourceNote(&motor.Turn{}, "svar")
	if len(sent) != 0 {
		t.Errorf("uten vellykket spørring skal noten utebli: %v", sent)
	}
}

// Observasjonen skal alltid tilhøre det resultatet svaret bygger på. En ny,
// mislykket spørring nullstiller den forrige (målt i legacy: en gammel
// observasjon ble hektet på feil svar).
func TestObservationBelongsToTheResultTheAnswerUses(t *testing.T) {
	f := newFloors("hvem er største kunde?", &motorTurnState{}, func(string) {})

	// Konsentrasjon krever minst fem rader og en SORTERT spørring — begge
	// deler er krav i insight.go, og observasjonen skal ikke utløses uten.
	rows := [][]string{
		{"Valhall", "900"}, {"Bifrost", "40"}, {"Utgard", "30"},
		{"Åsgard", "20"}, {"Midgard", "10"},
	}
	f.observeTurn(dbAttempt{outcome: dbOK, cols: []string{"kunde", "beløp"}, rows: rows},
		"SELECT kunde, beløp FROM salg ORDER BY beløp DESC", 20)
	if f.pending.kind == insNone {
		t.Fatal("en skjev fordeling skulle gitt en observasjon")
	}

	f.observeTurn(dbAttempt{outcome: dbFailed}, "SELECT 1", 20)
	if f.pending.kind != insNone {
		t.Error("en mislykket spørring skal nullstille observasjonen")
	}
}

// Svarbudsjettet: metodens tak vinner når den har et eget, ellers flytens.
func TestAnswerLimitPrefersMethodBudget(t *testing.T) {
	full := map[string]any{flowMaxField: 300}

	if got := motorAnswerLimit(motor.MethodRelation, full); got != 900 {
		t.Errorf("relasjonsklassen har eget tak 900, fikk %d", got)
	}
	if got := motorAnswerLimit(motor.MethodAnalysis, full); got != 300 {
		t.Errorf("analyse har ikke eget tak og skal arve flytens 300, fikk %d", got)
	}
	if got := motorAnswerLimit(motor.MethodNone, full); got != 300 {
		t.Errorf("uten metode skal flytens tak gjelde, fikk %d", got)
	}
}

// Den ærlige bunnen henter databasens EGEN redegjørelse, aldri en oppdiktet.
func TestDBExplainOnlyAfterRealFailure(t *testing.T) {
	f := newFloors("q", &motorTurnState{}, func(string) {})
	if got := f.dbExplain(); got != "" {
		t.Errorf("uten db-forsøk skal det ikke være noe å forklare, fikk %q", got)
	}

	f.state = &motorTurnState{dbTried: true, dbOK: true}
	if got := f.dbExplain(); got != "" {
		t.Errorf("etter vellykket spørring skal det ikke forklares, fikk %q", got)
	}

	f.state = &motorTurnState{dbTried: true, attempts: []dbAttempt{
		{outcome: dbNoAccess, sql: "SELECT * FROM hemmelig", conn: "Kunder"},
	}}
	if got := f.dbExplain(); got == "" {
		t.Error("etter feilet spørring skal redegjørelsen komme fra databasen")
	}
}
