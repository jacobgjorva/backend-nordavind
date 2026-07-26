package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

// Verktøykatalogen sendes på HVER runde i HVER tur. Vokteren fanger opp at et
// nytt eller utvidet verktøy stille gjør hver eneste melding dyrere.
func TestToolSizeBudget(t *testing.T) {
	budget := map[string]int{
		"web_search": 700, "fetch_url": 500, "show_table": 400,
		"m365_search": 650, "m365_read": 450,
		"mail_search": 850, "mail_read": 320, "mail_compose": 1300,
		"set_widget": 1800, "update_agent": 800, "setup_routine": 700,
		"save_m365_app": 700, "connect_database": 700, "connect_m365": 400,
		"check_m365": 300, "list_agents": 300, "delete_agent": 300,
	}
	all := []any{webSearchTool, fetchURLTool, showTableTool,
		m365SearchTool, m365ReadTool, mailSearchTool, mailReadTool, mailComposeTool}
	all = append(all, connectorAgentTools()...)
	all = append(all, buildAgentSetupTools()...)
	all = append(all, buildAgentEditTools()...)
	all = append(all, missionStartTools()...)
	all = append(all, widgetTools()...)

	for _, tool := range all {
		m, _ := tool.(map[string]any)
		fn, _ := m["function"].(map[string]any)
		name, _ := fn["name"].(string)
		b, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		max, ok := budget[name]
		if !ok {
			t.Errorf("verktøy %q mangler tokenbudsjett — legg det inn her (%d tegn)", name, len(b))
			continue
		}
		if len(b) > max {
			t.Errorf("verktøy %q er %d tegn, budsjett %d — kort ned beskrivelsen "+
				"eller hev budsjettet bevisst", name, len(b), max)
		}
	}
}

func TestConnectIntent(t *testing.T) {
	yes := []string{
		"kan du koble til databasen vår?",
		"vi bruker Postgres, får du tilgang?",
		"sett opp integrasjon mot SharePoint",
		"jeg vil koble på OneDrive",
		"her er client secret",
	}
	no := []string{
		"hva er styringsrenten nå?",
		"skriv en kort oppsummering av møtet",
		"hvor mange ordre kom inn i juni?",
		"takk!",
	}
	for _, s := range yes {
		if !connectIntent(s) {
			t.Errorf("connectIntent(%q) = false, ville mistet connector-verktøyene", s)
		}
	}
	for _, s := range no {
		if connectIntent(s) {
			t.Errorf("connectIntent(%q) = true, betaler for verktøy den ikke trenger", s)
		}
	}
}

func TestComposeIntent(t *testing.T) {
	yes := []string{
		"send en e-post til Kari om dette",
		"kan du svare på mailen fra Ola?",
		"videresend rapporten til styret",
		"lag et utkast til kunden",
	}
	no := []string{
		"hva står i den siste mailen fra Ola?",
		"søk opp e-poster om budsjettet",
		"oppsummer innboksen min",
		"hvor mange ordre kom inn i juni?",
	}
	for _, s := range yes {
		if !composeIntent(s) {
			t.Errorf("composeIntent(%q) = false, ville mistet mail_compose", s)
		}
	}
	for _, s := range no {
		if composeIntent(s) {
			t.Errorf("composeIntent(%q) = true, betaler for mail_compose unødig", s)
		}
	}
}

// recentTurn skal se forrige assistentsvar, så «ja, gjør det» etter at
// assistenten selv brakte tilkobling på bane fortsatt gir verktøyene.
func TestRecentTurnSeesPreviousAnswer(t *testing.T) {
	full := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "hei"},
		map[string]any{"role": "assistant", "content": "Hei! Hva kan jeg hjelpe med?"},
		map[string]any{"role": "user", "content": "vi har tall i et eget system"},
		map[string]any{"role": "assistant", "content": "Vil du at jeg kobler til databasen?"},
		map[string]any{"role": "user", "content": "ja, gjør det"},
	}}
	if !connectIntent(recentTurn(full)) {
		t.Error("bekreftelse etter assistentens forslag mistet connector-verktøyene")
	}
	// Men ikke lenger tilbake enn forrige tur.
	old := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "koble til databasen"},
		map[string]any{"role": "assistant", "content": "Ferdig."},
		map[string]any{"role": "user", "content": "hvor mange ordre i juni?"},
		map[string]any{"role": "assistant", "content": "426 ordre."},
		map[string]any{"role": "user", "content": "og i juli?"},
	}}
	if connectIntent(recentTurn(old)) {
		t.Error("gammel tilkoblingsprat drar verktøyene med i alle senere meldinger")
	}
}

// Kvadratisk-vekst-vernet: eldste verktøyresultater klippes når budsjettet
// sprenges, rundens ferske resultater fredes alltid.
func TestTrimToolHistory(t *testing.T) {
	big := strings.Repeat("x", 9000)
	mk := func(id string) map[string]any {
		return map[string]any{"role": "tool", "tool_call_id": id, "content": big}
	}
	fresh := map[string]any{"role": "tool", "tool_call_id": "fersk", "content": big}
	full := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "spørsmål"},
		mk("gammel1"), mk("gammel2"), mk("gammel3"),
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{}},
		fresh,
	}}
	trimToolHistory(full, 20000)

	if c, _ := fresh["content"].(string); len(c) != len(big) {
		t.Fatalf("ferskt resultat ble klippet — modellen har ikke lest det ennå")
	}
	msgs := full["messages"].([]any)
	first := msgs[1].(map[string]any)["content"].(string)
	if len(first) >= 9000 {
		t.Fatalf("eldste resultat skulle vært klippet, er fortsatt %d tegn", len(first))
	}
	// Uklippet under budsjett: en vanlig tur med to små resultater røres ikke.
	small := map[string]any{"role": "tool", "content": "42 rader"}
	full2 := map[string]any{"messages": []any{small, map[string]any{"role": "assistant", "content": "svar"}}}
	trimToolHistory(full2, 20000)
	if small["content"] != "42 rader" {
		t.Fatalf("lite resultat under budsjett ble endret")
	}
}

// LIGHT_TIER-bryteren: av → lette flyter kjører mid som før; på → LightModel.
func TestFlowModelLightTier(t *testing.T) {
	off := &Server{cfg: config.Config{LightTier: false}}
	on := &Server{cfg: config.Config{LightTier: true}}
	if got := off.flowModel("light"); got != router.MidModel {
		t.Errorf("LIGHT_TIER=off: light ga %q, skal være MidModel", got)
	}
	if got := on.flowModel("light"); got != router.LightModel {
		t.Errorf("LIGHT_TIER=on: light ga %q, skal være LightModel", got)
	}
	if got := on.flowModel("mid"); got != router.MidModel {
		t.Errorf("mid skal aldri påvirkes av bryteren, ga %q", got)
	}
}
