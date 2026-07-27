package api

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestPromptSizeReport skriver ut hvor mange tegn hvert verktøy og hver
// systemprompt legger på HVERT upstream-kall. Kjør med -v for rapporten.
func TestPromptSizeReport(t *testing.T) {
	size := func(v any) int {
		b, _ := json.Marshal(v)
		return len(b)
	}
	named := func(v any) string {
		m, _ := v.(map[string]any)
		f, _ := m["function"].(map[string]any)
		n, _ := f["name"].(string)
		return n
	}

	type row struct {
		name string
		n    int
	}
	var rows []row
	add := func(v any) { rows = append(rows, row{named(v), size(v)}) }

	add(webSearchTool)
	add(fetchURLTool)
	add(showTableTool)
	add(m365SearchTool)
	add(m365ReadTool)
	add(mailSearchTool)
	add(mailReadTool)
	add(mailComposeTool)
	for _, v := range connectorAgentTools() {
		add(v)
	}
	for _, v := range buildAgentSetupTools() {
		add(v)
	}
	for _, v := range buildAgentEditTools() {
		add(v)
	}
	for _, v := range missionStartTools() {
		add(v)
	}
	for _, v := range widgetTools() {
		add(v)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].n > rows[j].n })
	total := 0
	for _, r := range rows {
		total += r.n
		t.Logf("%6d  %s", r.n, r.name)
	}
	t.Logf("--- sum alle verktøy: %d tegn", total)

	for _, p := range []struct {
		name string
		s    string
	}{
		{"agentSetupSystem", agentSetupSystem},
		{"missionPlanSystem", missionPlanSystem},
		{"deepResearchSystem", deepResearchSystem},
		{"factJudgeSystem", factJudgeSystem},
		{"designSystemBase", designSystemBase},
		{"designSystemCompose", designSystemCompose},
		{"designSystemEdit", designSystemEdit},
	} {
		t.Logf("%6d  prompt %s", len([]rune(p.s)), p.name)
	}
}
