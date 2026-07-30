package api

import (
	"context"
	"encoding/json"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Verktøykjøreren for motor v6.
//
// Den gjør tre ting, og ingenting mer: håndhever budsjettet, kaller den
// EKSISTERENDE verktøykoden, og pakker resultatet i en typet konvolutt.
// All logikk om hvordan et søk gjøres eller en spørring valideres blir
// stående der den alt er målt (agent.go, dbtool.go, dbstrategy.go).
//
// KVOTEN HÅNDHEVES HER, ikke i prompten. Modellen fikk telle selv én gang
// og søkte elleve ganger på én tur (målt i prod). Når taket er nådd, får
// den en ærlig beskjed om å svare med det den har — aldri en feil, aldri
// stillhet.

// motorTools kobler motorens ToolRunner til serverens verktøy.
type motorTools struct {
	s     *Server
	dbCtx *dbToolCtx
	narr  *narrator
	emit  func(string)
	// turnState bærer det gulvene trenger etter turen (siste tabell,
	// db-forsøk). Motoren selv rører det aldri.
	state *motorTurnState
}

// motorTurnState er turens db-spor, som gulvene i del 5 bygger på.
type motorTurnState struct {
	attempts   []dbAttempt
	lastTable  string
	lastCols   []string
	lastRows   [][]string
	tableShown bool
	dbTried    bool
	dbOK       bool
}

// Run utfører ett verktøykall.
func (t *motorTools) Run(ctx context.Context, c motor.ToolCall, budget motor.Budget, turn *motor.Turn) motor.ToolResult {
	kind := motorToolKind(c.Name)

	// Budsjettet først: et kall som ikke har råd, utføres aldri.
	if !budget.Allow(kind, turn.Spent(kind)) {
		t.s.log.Info("motor v6: kvote brukt opp", "verktøy", c.Name, "metode", string(turn.Method))
		return motor.ToolResult{Text: quotaSpent, Kind: motor.KindOther}
	}

	var args struct {
		Query        string `json:"query"`
		URL          string `json:"url"`
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
	}
	_ = json.Unmarshal([]byte(c.Args), &args)

	t.narr.before(c.Name, c.Args)
	stop := t.narr.slow(c.Name)
	defer stop()

	switch c.Name {
	case "web_search":
		// Relevansankeret er spørsmålet PLUSS det modellen sist sa den
		// lette etter. Det er del 4s mekanisme; her sendes den bare med.
		text, srcs := t.s.runWebSearchFor(ctx, args.Query, motorRelevance(turn))
		t.narr.afterHits(c.Name, len(srcs))
		out := motor.ToolResult{Text: text, Kind: kind, Evidence: true}
		for _, r := range srcs {
			out.Sources = append(out.Sources, motor.Source{Title: r.Title, URL: r.URL})
		}
		return out

	case "fetch_url":
		text := t.s.runFetchURL(ctx, args.URL)
		t.narr.after(c.Name, text)
		return motor.ToolResult{Text: text, Kind: kind, Evidence: true}

	case "query_database":
		return t.runDB(ctx, c, args.ConnectionID, args.SQL, kind)

	case "show_table":
		if t.state.lastTable != "" && !t.state.tableShown {
			t.emit(contentSSE("\n\n```table\n" + t.state.lastTable + "\n```"))
			t.state.tableShown = true
			return motor.ToolResult{Text: "Tabellen er vist for brukeren.", Kind: kind}
		}
		return motor.ToolResult{Text: "Ingen nye data å vise — kjør query_database først.", Kind: kind}
	}

	// Verktøy med egen leveransekontrakt (compose-kort, widget-blokk,
	// rutine-kvittering) eies av agent-løkka. Motoren svarer ALDRI «ukjent
	// verktøy» — da skriver modellen e-posten som ren tekst i chatten
	// (målt). Hele turen gis videre i stedet.
	return motor.ToolResult{Handoff: true}
}

// runDB kjører spørringen og oversetter det typede utfallet til tekst
// modellen kan stole på.
func (t *motorTools) runDB(ctx context.Context, c motor.ToolCall, connID, sql string,
	kind motor.ToolKind) motor.ToolResult {

	t.state.dbTried = true
	att := t.s.runDBQuery(ctx, t.dbCtx, connID, sql)
	t.state.attempts = append(t.state.attempts, att)

	text := att.text
	if att.note != "" {
		text += "\n" + att.note
	}
	if att.ok() {
		t.state.dbOK = true
		t.state.lastTable, t.state.lastCols, t.state.lastRows = att.text, att.cols, att.rows
		// Aggregering FØR modellen ser radene: lange resultatsett leses
		// ikke, de resonneres bort (målt: fant ikke linje 317 av 400).
		if len(att.rows) > motor.MaxRawRows {
			text = motor.SummarizeRows(att.cols, att.rows)
		}
	}
	// Et tomt aggregat er ÉN rad med 0 og blank celle. Får modellen den
	// tabellen, leser den den som et beløp og pynter på det — målt: «Null
	// kroner og elleve øre» der svaret var «ingen ordrer». Klartekst i
	// stedet, så det ikke finnes noe å tolke.
	if att.outcome == dbEmpty {
		text = "Spørringen kjørte fint, men traff INGEN rader. Det finnes altså ingen " +
			"data som matcher filteret — ikke et beløp på null. Si det slik til brukeren."
		if att.note != "" {
			text += "\n" + att.note
		}
	}
	t.narr.afterDB(att, att.text)
	return motor.ToolResult{Text: text, Kind: kind, Evidence: true}
}

// motorRelevance er vektoren kildeutdragene rangeres mot. Selve regelen
// bor i motor-pakken (thought.go), så api-laget ikke kan drifte fra den.
func motorRelevance(turn *motor.Turn) string {
	return motor.Relevance(turn.Question, turn.LastThought())
}
