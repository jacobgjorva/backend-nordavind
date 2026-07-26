package api

import (
	"testing"

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
	} {
		if toolByName(c.list, c.name) == nil {
			t.Errorf("verktøy %q finnes ikke i byggeren", c.name)
		}
	}
}
