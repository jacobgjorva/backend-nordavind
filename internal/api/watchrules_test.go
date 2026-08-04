package api

import (
	"strings"
	"testing"
)

func sqlStep(raw string) stepResult { return stepResult{label: "test", kind: "sql", raw: raw} }

const marginRaw = `{"columns":["kunde","margin"],"rows":[["Cask AS","42100"],["Ferment AS","61000"]]}`

func TestWatchRuleThresholdFires(t *testing.T) {
	rules := []watchRule{{Step: 1, Column: "margin", Agg: "min", Op: "<", Value: 50000, Label: "laveste margin"}}
	alert, findings, blind := evaluateWatchRules(rules, []stepResult{sqlStep(marginRaw)}, "")
	if !alert || len(blind) != 0 {
		t.Fatalf("regelen skulle utløst varsel, alert=%v blind=%v", alert, blind)
	}
	if len(findings) != 1 || !strings.Contains(findings[0], "42 100") || !strings.Contains(findings[0], "50 000") {
		t.Fatalf("Funn-linja skal ha målt verdi og terskel, fikk %v", findings)
	}
}

func TestWatchRuleThresholdHolds(t *testing.T) {
	rules := []watchRule{{Step: 1, Column: "margin", Agg: "min", Op: "<", Value: 40000}}
	if alert, findings, _ := evaluateWatchRules(rules, []stepResult{sqlStep(marginRaw)}, ""); alert || len(findings) != 0 {
		t.Fatalf("regelen skulle IKKE utløst varsel, fikk %v", findings)
	}
}

func TestWatchRuleCountOnEmptyRows(t *testing.T) {
	raw := `{"columns":["ordre"],"rows":[]}`
	rules := []watchRule{{Step: 1, Agg: "count", Op: "<", Value: 1, Label: "nye ordre"}}
	if alert, _, _ := evaluateWatchRules(rules, []stepResult{sqlStep(raw)}, ""); !alert {
		t.Fatal("count på tomt resultat skal kunne utløse varsel")
	}
}

func TestWatchRuleUnknownColumnIsBlindNotAlert(t *testing.T) {
	rules := []watchRule{{Step: 1, Column: "finnes_ikke", Op: ">", Value: 1}}
	alert, _, blind := evaluateWatchRules(rules, []stepResult{sqlStep(marginRaw)}, "")
	if alert || len(blind) != 1 {
		t.Fatalf("umålbar regel skal være blind, aldri varsle: alert=%v blind=%v", alert, blind)
	}
}

func TestWatchRulePctSkipsFirstRun(t *testing.T) {
	rules := []watchRule{{Step: 1, Column: "margin", Agg: "sum", Op: "<", Value: -15, Pct: true}}
	alert, _, blind := evaluateWatchRules(rules, []stepResult{sqlStep(marginRaw)}, "")
	if alert || len(blind) != 0 {
		t.Fatalf("pct-regel uten forrige kjøring skal hoppe over stille, alert=%v blind=%v", alert, blind)
	}
}

func TestWatchRulePctFiresOnDrop(t *testing.T) {
	prev := `[{"label":"test","data":"{\"columns\":[\"kunde\",\"margin\"],\"rows\":[[\"Cask AS\",\"100000\"],[\"Ferment AS\",\"30000\"]]}"}]`
	rules := []watchRule{{Step: 1, Column: "margin", Agg: "sum", Op: "<", Value: -15, Pct: true, Label: "samlet margin"}}
	alert, findings, _ := evaluateWatchRules(rules, []stepResult{sqlStep(marginRaw)}, prev)
	// 130 000 → 103 100 = -20,7 % — under -15.
	if !alert || len(findings) != 1 || !strings.Contains(findings[0], "%") {
		t.Fatalf("pct-fall skulle varslet med prosent i Funn-linja, fikk alert=%v %v", alert, findings)
	}
}

func TestValidateWatchRulesCatchesBadColumnAndStep(t *testing.T) {
	plan := agentPlan{Steps: []agentStep{{Kind: "sql", Label: "margin"}, {Kind: "web", Label: "nyheter"}}}
	trial := map[int]string{1: marginRaw}
	problems := validateWatchRules([]watchRule{
		{Step: 1, Column: "finnes_ikke", Op: ">", Value: 1},
		{Step: 2, Column: "margin", Op: ">", Value: 1},
		{Step: 9, Column: "margin", Op: ">", Value: 1},
		{Step: 1, Column: "margin", Op: "~", Value: 1},
	}, plan, trial)
	if len(problems) != 4 {
		t.Fatalf("ventet 4 problemer (kolonne, ikke-sql-steg, ukjent steg, ukjent op), fikk %d: %v", len(problems), problems)
	}
}

func TestGateAgentSummaryBlocksInventedName(t *testing.T) {
	basis := []string{marginRaw}
	if gateAgentSummary("Marginen hos Zorbatron Industrier har falt kraftig.", basis) {
		t.Fatal("diktet navn i agent-rapport skulle vært holdt tilbake")
	}
	if !gateAgentSummary("Samlet margin er 103 100, ned 20,7 % siden sist.", basis) {
		t.Fatal("beregnede tall skal slippe gjennom med tabellen som kvittering")
	}
}

func TestFunnBlockDeterministic(t *testing.T) {
	out := funnBlock([]string{"laveste margin: 42 100 (regel: < 50 000)"})
	if !strings.HasPrefix(out, "**Funn:**") || !strings.Contains(out, "42 100") {
		t.Fatalf("uventet funn-blokk: %q", out)
	}
	if funnBlock(nil) != "" {
		t.Fatal("ingen funn skal gi tom blokk")
	}
}
