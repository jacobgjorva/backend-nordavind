package api

import (
	"context"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/deck"
	"github.com/jacobgjorva/backend-nordavind/internal/intent"
)

// Helgardering: HVER ikke-deterministisk flyt i tabellen skal kun bruke
// verktøynavn flowTools kan løse. Et nytt navn uten resolver-case skal
// feile her — aldri oppdages av en bruker som står uten verktøy.
func TestAllFlowToolsResolvable(t *testing.T) {
	resolvable := map[string]bool{
		intent.ToolWebSearch: true, intent.ToolFetchURL: true,
		intent.ToolQueryDatabase: true, intent.ToolShowTable: true,
		intent.ToolM365Search: true, intent.ToolM365Read: true,
		intent.ToolMailSearch: true, intent.ToolMailRead: true, intent.ToolMailCompose: true,
		intent.ToolConnectDB: true, intent.ToolConnectM365: true,
		intent.ToolCheckM365: true, intent.ToolSaveM365App: true,
		intent.ToolSetupRoutine: true, intent.ToolListAgents: true,
		intent.ToolUpdateAgent: true, intent.ToolSetWidget: true,
		intent.ToolSetSlide: true, intent.ToolSetDeck: true,
		intent.ToolContactPerson: true,
	}
	for key, f := range intent.Flows {
		if f.Deterministic {
			continue
		}
		for _, name := range f.Tools {
			if !resolvable[name] {
				t.Errorf("flyt %s bruker verktøy %q uten resolver i flowTools", key, name)
			}
		}
	}
}

// Navnene i resolver-listene skal faktisk finnes i verktøybyggerne.
func TestToolByNameFindsBuilders(t *testing.T) {
	for _, c := range []struct {
		name string
		list []any
	}{
		{intent.ToolConnectDB, connectorAgentTools()},
		{intent.ToolCheckM365, connectorAgentTools()},
		{intent.ToolSaveM365App, connectorAgentTools()},
		{intent.ToolConnectM365, connectorAgentTools()},
		{intent.ToolSetupRoutine, missionStartTools()},
		{intent.ToolListAgents, buildAgentSetupTools()},
		{intent.ToolUpdateAgent, buildAgentEditTools()},
		{intent.ToolSetWidget, widgetTools()},
		{intent.ToolSetSlide, deckTools(deck.Default)},
		{intent.ToolSetDeck, deckTools(deck.Default)},
	} {
		if toolByName(c.list, c.name) == nil {
			t.Errorf("verktøy %q finnes ikke i byggeren", c.name)
		}
	}
}

// Uklart skal gi et TOMT verktøysett — ikke fail-open til fritt sett.
// Uten verktøy kan modellen ikke søke seg til en gjetning; det er hele
// grunnen til at flyten finnes.
func TestUnclearFlowGetsNoTools(t *testing.T) {
	s := &Server{}
	tools, ok := s.flowTools(context.Background(), intent.UnclearKey, nil)
	if !ok {
		t.Fatal("uklart skal kunne innfris (ok=false gir fritt sett)")
	}
	if len(tools) != 0 {
		t.Fatalf("uklart skal ha null verktøy, fikk %d", len(tools))
	}
}
