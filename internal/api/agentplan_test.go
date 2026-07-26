package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// En plan uten steg, watch eller alert_rule skal avvises med presise problemer
// modellen kan rette.
func TestValidatePlanKreverStegOgRegler(t *testing.T) {
	s := &Server{}
	_, problems := s.validatePlan(context.Background(), nil, `{"steps":[]}`)
	if len(problems) != 3 {
		t.Fatalf("ventet 3 problemer (steg, watch, alert_rule), fikk %d: %v", len(problems), problems)
	}
}

// Ukjent steg-type og manglende felt skal fanges før planen lagres.
func TestValidatePlanFangerUgyldigeSteg(t *testing.T) {
	s := &Server{}
	args, _ := json.Marshal(map[string]any{
		"watch":      "omsetning",
		"alert_rule": "avvik over 10 %",
		"steps": []map[string]any{
			{"kind": "web", "label": "søk", "query": "omsetning"}, // gyldig
			{"kind": "web", "label": "tomt søk"},                  // mangler query
			{"kind": "telepati", "label": "?"},                    // ukjent type
		},
	})
	plan, problems := s.validatePlan(context.Background(), nil, string(args))
	if len(plan.Steps) != 3 {
		t.Fatalf("ventet 3 parsede steg, fikk %d", len(plan.Steps))
	}
	if len(problems) != 2 {
		t.Fatalf("ventet 2 problemer, fikk %d: %v", len(problems), problems)
	}
	if !strings.Contains(strings.Join(problems, " "), "telepati") {
		t.Errorf("ukjent steg-type ble ikke rapportert: %v", problems)
	}
}

// Uten database skal sql-steg avvises i stedet for å lagres og feile hver kjøring.
func TestValidatePlanAvviserSQLUtenDatabase(t *testing.T) {
	s := &Server{}
	args, _ := json.Marshal(map[string]any{
		"watch":      "rader",
		"alert_rule": "nye rader",
		"steps":      []map[string]any{{"kind": "sql", "label": "hent", "sql": "SELECT 1"}},
	})
	_, problems := s.validatePlan(context.Background(), nil, string(args))
	if len(problems) != 1 || !strings.Contains(problems[0], "ingen database") {
		t.Fatalf("ventet database-problem, fikk: %v", problems)
	}
}

// Snapshotet lagres som JSON, men mates til modellen i samme format som ferske
// data — ellers ville «forrige» og «nå» sett ulike ut av feil grunn.
func TestSnapshotTextGjenskaperStegformat(t *testing.T) {
	raw, _ := json.Marshal([]map[string]string{
		{"label": "Omsetning siste 7 dager", "data": `{"rows":[[1200]]}`},
		{"label": "Åpne avvik", "data": `{"rows":[[3]]}`},
	})
	got := snapshotText(string(raw))
	for _, want := range []string{
		"### Steg 1: Omsetning siste 7 dager",
		`{"rows":[[1200]]}`,
		"### Steg 2: Åpne avvik",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mangler %q i:\n%s", want, got)
		}
	}
}

// Et snapshot som ikke lar seg parse skal fortsatt gi noe brukbart, ikke tomt.
func TestSnapshotTextTalerUgyldigJSON(t *testing.T) {
	if got := snapshotText("ikke json"); got != "ikke json" {
		t.Errorf("ventet uendret tekst, fikk %q", got)
	}
}

// Rapporten skal vise nøkkeltall som kort, med delta, og tabellen skal rendres
// fra stegets rådata — ikke fra noe modellen skriver av.
func TestComposeReportByggerKortOgTabell(t *testing.T) {
	steps := []stepResult{
		{label: "Avvik", kind: "sql", raw: `{"columns":["dato","beløp"],"rows":[["01.07","1200"]]}`},
	}
	got := composeReport("Ett nytt avvik.", []reportStat{
		{Label: "Omsetning", Value: "1 200", Unit: "kr", Delta: "+12 %"},
	}, 1, steps, "")

	for _, want := range []string{"```stat", `"delta":"+12 %"`, "Ett nytt avvik.", "```table", `"dato"`} {
		if !strings.Contains(got, want) {
			t.Errorf("mangler %q i:\n%s", want, got)
		}
	}
}

// Uten stats og uten tabellsteg skal rapporten være ren tekst.
func TestComposeReportUtenPynt(t *testing.T) {
	got := composeReport("Ingenting å melde.", nil, 0, nil, "")
	if got != "Ingenting å melde." {
		t.Errorf("ventet ren tekst, fikk %q", got)
	}
}

// Et tabellsteg utenfor rekkevidde skal ignoreres, ikke krasje kjøringen.
func TestComposeReportIgnorererUgyldigTabellsteg(t *testing.T) {
	steps := []stepResult{{label: "A", kind: "sql", raw: `{"columns":["x"],"rows":[["1"]]}`}}
	for _, step := range []int{-1, 0, 2, 99} {
		got := composeReport("Tekst.", nil, step, steps, "")
		if strings.Contains(got, "```table") {
			t.Errorf("steg %d ga tabell den ikke skulle: %s", step, got)
		}
	}
}

// Maks fire kort, ellers drukner meldingen i nøkkeltall.
func TestComposeReportBegrenserAntallKort(t *testing.T) {
	stats := make([]reportStat, 7)
	for i := range stats {
		stats[i] = reportStat{Label: "K", Value: "1"}
	}
	if n := strings.Count(composeReport("Tekst.", stats, 0, nil, ""), "```stat"); n != 4 {
		t.Errorf("ventet 4 kort, fikk %d", n)
	}
}

// Grafen vises som en widget-blokk, så den henter ferske tall selv.
func TestComposeReportTarMedGraf(t *testing.T) {
	got := composeReport("Omsetningen stiger.", nil, 0, nil, "salg-a1b2c3d4")
	if !strings.Contains(got, "```widget\nsalg-a1b2c3d4\n```") {
		t.Errorf("mangler widget-blokk i:\n%s", got)
	}
}

// En graf uten x eller y ville fått en umerket akse — det skal avvises.
func TestValidateChartKreverBeggeAkser(t *testing.T) {
	s := &Server{}
	problems := s.validateChart(context.Background(), nil, &agentChart{
		Type: "line", Title: "Salg", SQL: "SELECT 1", X: "dato",
	})
	if !strings.Contains(strings.Join(problems, " "), "både x og y") {
		t.Errorf("manglende y ble ikke fanget: %v", problems)
	}
}

// Ukjent graftype skal fanges før planen lagres.
func TestValidateChartAvviserUkjentType(t *testing.T) {
	s := &Server{}
	problems := s.validateChart(context.Background(), nil, &agentChart{
		Type: "kakediagram", Title: "Salg", SQL: "SELECT 1", X: "dato", Y: "sum",
	})
	if !strings.Contains(strings.Join(problems, " "), "ukjent type") {
		t.Errorf("ukjent graftype ble ikke fanget: %v", problems)
	}
}

// Uten graf skal valideringen ikke ha noe å si.
func TestValidateChartUtenGrafErStille(t *testing.T) {
	s := &Server{}
	if p := s.validateChart(context.Background(), nil, nil); len(p) != 0 {
		t.Errorf("ventet ingen problemer, fikk %v", p)
	}
}

// Slug'en må være stabil og unik per agent, så en ny plan overtar samme graf.
func TestChartSlugErStabilOgUnik(t *testing.T) {
	a := store.Agent{ID: "abcdef1234567890", Name: "Salg Nord"}
	got := chartSlug(a)
	if got != "salg-nord-abcdef12" {
		t.Fatalf("uventet slug: %q", got)
	}
	if chartSlug(a) != got {
		t.Error("slug er ikke stabil mellom kall")
	}
	navnløs := chartSlug(store.Agent{ID: "xyz12345", Name: ""})
	if navnløs != "agent-xyz12345" {
		t.Errorf("uventet slug uten navn: %q", navnløs)
	}
}

// Et web-steg har ingen rader å rendre — da skal det ikke bli tabell.
func TestTableFromStepKunSQL(t *testing.T) {
	web := stepResult{label: "Søk", kind: "web", raw: `{"columns":["a"],"rows":[["1"]]}`}
	if tableFromStep(web) != "" {
		t.Error("web-steg ga tabell")
	}
	tom := stepResult{label: "Tom", kind: "sql", raw: `{"columns":["a"],"rows":[]}`}
	if tableFromStep(tom) != "" {
		t.Error("tomt resultat ga tabell")
	}
}

// runDBQuery svarer med tekst ved feil og JSON ved suksess — plan-validering
// og -kjøring skiller på nettopp det.
func TestPlanQueryFailed(t *testing.T) {
	if planQueryFailed(`{"columns":["a"],"rows":[],"row_count":0}`) {
		t.Error("gyldig JSON-svar ble regnet som feil")
	}
	if !planQueryFailed("Spørringen feilet: no such column: omsetning") {
		t.Error("feilmelding ble regnet som suksess")
	}
	if !planQueryFailed("") {
		t.Error("tomt svar ble regnet som suksess")
	}
}
