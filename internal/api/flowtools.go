package api

import (
	"context"

	"github.com/jacobgjorva/backend-nordavind/internal/design"
	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Flyt → verktøy: tabelldrevet oppslag fra flytens Tools-navn (flows.go) til
// faktiske verktøydefinisjoner. Erstattet switch-per-flyt som lot nye flyter
// stå uten verktøy (m365_files-bommen). Ukjent navn eller utilgjengelig
// avhengighet = fail-open til fritt chat-sett — modellen skal ALDRI stå
// verktøyløs i en flyt som lover verktøy.

// toolByName plukker verktøyet med gitt funksjonsnavn fra en liste.
func toolByName(list []any, name string) any {
	for _, t := range list {
		if m, ok := t.(map[string]any); ok {
			if fn, ok := m["function"].(map[string]any); ok && fn["name"] == name {
				return t
			}
		}
	}
	return nil
}

// flowTools løser en flyts verktøynavn. ok=false betyr at flyten ikke kan
// innfris (manglende db/M365) — kalleren skal falle tilbake til fritt sett.
func (s *Server) flowTools(ctx context.Context, flowKey string, dbCtx *dbToolCtx) ([]any, bool) {
	flow, known := intent.Flows[flowKey]
	if !known {
		return nil, false
	}
	tools := make([]any, 0, len(flow.Tools))
	for _, name := range flow.Tools {
		switch name {
		case intent.ToolWebSearch:
			tools = append(tools, webSearchTool)
		case intent.ToolFetchURL:
			tools = append(tools, fetchURLTool)
		case intent.ToolQueryDatabase:
			if dbCtx == nil {
				return nil, false
			}
			tools = append(tools, dbCtx.tool)
		case intent.ToolShowTable:
			if dbCtx == nil {
				return nil, false
			}
			tools = append(tools, showTableTool)
		case intent.ToolM365Search, intent.ToolM365Read, intent.ToolMailSearch, intent.ToolMailRead, intent.ToolMailCompose:
			if _, ok := s.m365Connected(ctx); !ok {
				return nil, false
			}
			switch name {
			case intent.ToolM365Search:
				tools = append(tools, m365SearchTool)
			case intent.ToolM365Read:
				tools = append(tools, m365ReadTool)
			case intent.ToolMailSearch:
				tools = append(tools, mailSearchTool)
			case intent.ToolMailRead:
				tools = append(tools, mailReadTool)
			case intent.ToolMailCompose:
				tools = append(tools, mailComposeTool)
			}
		case intent.ToolConnectDB, intent.ToolConnectM365, intent.ToolCheckM365, intent.ToolSaveM365App:
			if t := toolByName(connectorAgentTools(), name); t != nil {
				tools = append(tools, t)
			} else {
				return nil, false
			}
		case intent.ToolSetupRoutine:
			if t := toolByName(missionStartTools(), name); t != nil {
				tools = append(tools, t)
			} else {
				return nil, false
			}
		case intent.ToolListAgents:
			if t := toolByName(buildAgentSetupTools(), name); t != nil {
				tools = append(tools, t)
			} else {
				return nil, false
			}
		case intent.ToolUpdateAgent:
			if t := toolByName(buildAgentEditTools(), name); t != nil {
				tools = append(tools, t)
			} else {
				return nil, false
			}
		case intent.ToolCompose, intent.ToolPatch, intent.ToolRestyle:
			if t := toolByName(designTools(design.Get(design.Default)), name); t != nil {
				tools = append(tools, t)
			} else {
				return nil, false
			}
		case intent.ToolSetWidget:
			if t := toolByName(widgetTools(), name); t != nil {
				tools = append(tools, t)
			} else {
				return nil, false
			}
		case intent.ToolContactPerson:
			// Valgfritt: finnes kun når tenanten har kontaktregister.
			if user, ok := ctx.Value(userKey).(store.User); ok {
				if ct := s.contactTool(user.TenantID); ct != nil {
					tools = append(tools, ct)
				}
			}
		default:
			// Ukjent navn i tabellen: programmeringsfeil — fail-open.
			s.log.Warn("flyt-verktøy: ukjent navn", "flow", flowKey, "tool", name)
			return nil, false
		}
	}
	return tools, true
}
