package api

import (
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Gulvene for motor v6, koblet til den EKSISTERENDE, målte koden.
//
// Ingenting her er ny logikk. Innsiktslaget (insight.go), kildenoten
// (dbstrategy.go) og tabellgarantien er alle skrevet og verifisert for
// legacy-løypa; denne fila oppfyller bare motorens Floors-kontrakt ved å
// kalle dem. Flyttes eller endres et gulv, skjer det ett sted.
//
// Hvert gulv utløses av et FAKTUM turen etterlot seg: kom det rader, ble
// tabellen vist, har tenanten flere tilkoblinger. Aldri av hvordan svaret
// er formulert.

// motorFloors bærer det turen etterlot seg og det som trengs for å levere.
type motorFloors struct {
	s     *Server
	emit  func(string)
	narr  *narrator
	state *motorTurnState
	// multiSource: tenanten har mer enn én tilkobling, så et svar må si
	// hvilken det bygger på.
	multiSource bool
	// question er brukerens FAKTISKE spørsmål, fanget ved turstart.
	question string
	// pending er kodens observasjon om datagrunnlaget, regnet ut da
	// spørringen kjørte (observe i insight.go).
	pending insight
}

// Table viser turens tabell hvis den finnes, ikke alt er vist, og brukeren
// faktisk ba om en. tableIntent leser BESTILLINGEN, ikke svaret.
func (f *motorFloors) Table(_ *motor.Turn) {
	if f.state.lastTable == "" || f.state.tableShown || !tableIntent(f.question) {
		return
	}
	f.emit(contentSSE("\n\n```table\n" + f.state.lastTable + "\n```"))
	f.state.tableShown = true
}

// Insight leverer kodens observasjon som én setning. Alle portene mot støy
// ligger i deliverInsight (insight.go) og er målt der.
func (f *motorFloors) Insight(_ *motor.Turn, answer string, limit int) {
	f.narr.deliverInsight(f.emit, f.pending, answer, limit, f.state.tableShown)
}

// SourceNote navngir kilden når tenanten har flere. Vakten mot dobbel note
// ligger i narratoren (målt: «Tallene er fra X. Tallene er fra X.»).
func (f *motorFloors) SourceNote(_ *motor.Turn, answer string) {
	if !f.state.dbOK {
		return
	}
	f.narr.sourceNote(f.emit, answer, usedSources(f.state.attempts), f.multiSource)
}

// NextStep er ikke portert ennå: laget finnes bare på engine-experiment, og
// å kopiere det hit ville vært å gjenskape umålt kode. Gulvet står som en
// eksplisitt åpning i stedet for å late som det er dekket.
func (f *motorFloors) NextStep(_ *motor.Turn, _ string) {}

// observeTurn regner kodens observasjon av et databaseresultat. Kalles når
// spørringen kjører, mens radene finnes i minnet.
//
// Nullstilles ved hver ny spørring: observasjonen skal alltid tilhøre det
// resultatet svaret faktisk bygger på (målt i legacy — en tidligere
// spørrings observasjon hang igjen og ble hektet på feil svar).
func (f *motorFloors) observeTurn(att dbAttempt, sql string, rowCap int) {
	if !att.ok() {
		f.pending = insight{}
		return
	}
	if o, ok := observe(att.cols, att.rows, sql, f.question, rowCap, time.Now()); ok {
		f.pending = o
		return
	}
	f.pending = insight{}
}

// answerLimit gir svarbudsjettet: metodens tak når den har et eget, ellers
// flytens (som i dag).
func motorAnswerLimit(method motor.MethodKey, full map[string]any) int {
	if m, ok := motor.Get(method); ok && m.Budget.MaxChars > 0 {
		return m.Budget.MaxChars
	}
	return answerCharLimit(full)
}

// dbExplain er databasens egen redegjørelse når alle forsøk feilet — den
// ærlige bunnen henter teksten herfra i stedet for å finne på en.
func (f *motorFloors) dbExplain() string {
	if !f.state.dbTried || f.state.dbOK {
		return ""
	}
	return strings.TrimSpace(explainAttempts(f.state.attempts, nil))
}
