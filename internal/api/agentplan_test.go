package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
