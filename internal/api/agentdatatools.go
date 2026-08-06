package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Chat-verktøyene for agentens datalager. All SQL genereres i store-laget fra
// planens skjema — modellen sender bare tabellnavn og verdier, aldri SQL.

// planDatastore leser datastore-skjemaet ut av en agents lagrede plan.
func planDatastore(a store.Agent) []store.DataTable {
	if strings.TrimSpace(a.Plan) == "" {
		return nil
	}
	var p struct {
		Datastore []store.DataTable `json:"datastore"`
	}
	if err := json.Unmarshal([]byte(a.Plan), &p); err != nil {
		return nil
	}
	return p.Datastore
}

// datastoreSystem beskriver lageret for modellen i agent-chatten.
func datastoreSystem(ds []store.DataTable) string {
	var b strings.Builder
	b.WriteString("Agenten har et EGET datalager brukeren kan vedlikeholde via deg: ")
	for i, t := range ds {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(t.Name + "(")
		for j, c := range t.Columns {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(c.Name + " " + c.Type)
		}
		b.WriteString(")")
		if t.Key != "" {
			b.WriteString(" nøkkel=" + t.Key)
		}
	}
	b.WriteString(". Ber brukeren om å legge til, endre eller trekke fra noe i lageret: bruk " +
		"data_upsert (hele raden, med nøkkelen satt). Sletting: data_delete med filter. Visning: " +
		"data_list og vis radene med show_table-stil tabell fra verktøysvaret. Regn selv ut nye " +
		"beholdninger fra gjeldende verdi i data_list før du oppdaterer.")
	return b.String()
}

// buildDatastoreTools gir chat-verktøyene for lageret.
func buildDatastoreTools() []any {
	obj := func(desc string) map[string]any {
		return map[string]any{"type": "object", "description": desc,
			"additionalProperties": map[string]any{"type": "string"}}
	}
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "data_list",
				"description": "Les en tabell fra agentens datalager.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"table": map[string]any{"type": "string"}},
					"required":   []string{"table"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "data_upsert",
				"description": "Legg til eller oppdater ÉN rad i agentens datalager. Send alle verdier som tekst.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"table":  map[string]any{"type": "string"},
						"values": obj("kolonne→verdi for raden, inkludert nøkkelen"),
					},
					"required": []string{"table", "values"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "data_delete",
				"description": "Slett rader som matcher likhetsfiltre. Krever minst ett filter — bekreft med brukeren før du sletter flere rader enn én.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"table": map[string]any{"type": "string"},
						"where": obj("kolonne→verdi som må stemme for at raden slettes"),
					},
					"required": []string{"table", "where"},
				},
			},
		},
	}
}

type dataToolArgs struct {
	Table  string            `json:"table"`
	Values map[string]any    `json:"values"`
	Where  map[string]any    `json:"where"`
}

func stringMap(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			// Råformat, ikke visningsformat — verdien skal inn i en typet kolonne.
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			out[k] = fmt.Sprintf("%t", t)
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprintf("%v", t)
		}
	}
	return out
}

// runDataTool utfører ett datalager-kall for agent-chatten.
func (s *Server) runDataTool(_ context.Context, name, agentID string, ds []store.DataTable, rawArgs string) string {
	var a dataToolArgs
	_ = json.Unmarshal([]byte(rawArgs), &a)
	switch name {
	case "data_list":
		cols, rows, err := s.store.ReadAgentTable(agentID, ds, a.Table)
		if err != nil {
			return "Datalager-feil: " + err.Error()
		}
		raw, _ := json.Marshal(map[string]any{"columns": cols, "rows": rows})
		return string(raw)
	case "data_upsert":
		vals := stringMap(a.Values)
		if err := s.store.UpsertAgentRow(agentID, ds, a.Table, vals); err != nil {
			return "Kunne ikke lagre: " + err.Error()
		}
		return "Lagret i " + a.Table + "."
	case "data_delete":
		n, err := s.store.DeleteAgentRows(agentID, ds, a.Table, stringMap(a.Where))
		if err != nil {
			return "Kunne ikke slette: " + err.Error()
		}
		return fmt.Sprintf("Slettet %d rad(er) fra %s.", n, a.Table)
	}
	return "Ukjent datalager-verktøy."
}
