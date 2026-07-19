package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// agentSetupSystem er instruksen som legges inn når brukeren starter
// /agent-flyten. Den styrer hvordan modellen samler inn oppsett og kaller
// verktøyene — holdt server-side så det er én kilde til sannhet.
const agentSetupSystem = "Brukeren vil sette opp en agent: en automatisert prosedyre som kjører på " +
	"fast frekvens. Ditt FØRSTE svar skal være ÉN kort setning som kun spør hva agenten skal gjøre " +
	"(f.eks. «Hva skal agenten gjøre for deg?») — ingen tilleggsspørsmål om data, nettsider, " +
	"database, frekvens eller tilgang. Utled selv om det er en database- eller websøk-oppgave fra svaret. " +
	"Deretter, ett spørsmål av gangen, samle inn: hvor ofte den skal kjøre og på hvilket klokkeslett, " +
	"og — HVIS det er en database-oppgave — hvilken tilkobling (bruk navnet, aldri finn opp en id). " +
	"MINIMUM intervall er 15 minutter (900 sek). Ber brukeren om noe hyppigere, forklar kort at 15 " +
	"minutter er minste tillatte og foreslå det. " +
	"En websøk-agent trenger ingen tilkobling. Ikke vis widgeten før du har frekvens og tidspunkt. " +
	"Når du har alt, svar med én kort " +
	"bekreftelsessetning etterfulgt av en ```agent_setup kodeblokk med JSON:\n" +
	"{\"name\":\"kort navn\",\"task\":\"presis instruks\",\"fields\":[ ... ]}\n" +
	"fields er en liste over kontrollene widgeten skal vise — ta KUN med de relevante, hver med en " +
	"fornuftig forhåndsverdi:\n" +
	"- {\"type\":\"interval\",\"value\":<sekunder; 3600=time, 86400=dag, 172800=annenhver dag, 604800=uke — bruk vilkårlig antall>}\n" +
	"- {\"type\":\"time\",\"value\":\"HH:MM\"} (kun hvis intervall er minst én dag)\n" +
	"- {\"type\":\"effort\",\"value\":\"Lav|Moderat|Høy|Max\"}\n" +
	"- {\"type\":\"connection\",\"value\":\"<connection_id>\"} (KUN database-agenter)\n" +
	"- {\"type\":\"write\",\"value\":false} (KUN database-agenter som kan trenge skriving)\n" +
	"Ta alltid med interval og effort. Utelat connection og write for websøk-agenter. Ikke skriv " +
	"noe etter kodeblokken. Hvis brukeren vil se agentene sine, kall list_agents og gjengi som en " +
	"```agents kodeblokk."

// agentEditSystem legges inn i en agent-chat så modellen kan endre agenten
// når brukeren ber om det.
func agentEditSystem(a store.Agent) string {
	return fmt.Sprintf(
		"Dette er chatten til agenten «%s» (id=%s). Nåværende oppsett: oppgave=%q, "+
			"frekvens=%q, intervall=%d sek, token-grense=%d, skrivetilgang=%t. "+
			"Hvis brukeren ber om å endre agenten (oppgave, frekvens, klokkeslett, innsats/token-grense "+
			"eller tilgang), kall update_agent med agent_id=%s og kun feltene som endres. Bekreft kort "+
			"hva du endret. Minimum intervall er 900 sek (15 min). Ellers svar normalt.",
		a.Name, a.ID, a.Task, a.ScheduleLabel, a.IntervalSeconds, a.DailyTokenLimit, a.WriteAccess, a.ID,
	)
}

// buildAgentEditTools gir modellen verktøy til å endre/slette den ene agenten.
func buildAgentEditTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "update_agent",
				"description": "Endre en eksisterende agents oppsett. Send agent_id og kun feltene som skal endres.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agent_id":          map[string]any{"type": "string"},
						"name":              map[string]any{"type": "string", "description": "nytt navn (valgfritt)"},
						"task":              map[string]any{"type": "string", "description": "ny oppgave (valgfritt)"},
						"schedule_label":    map[string]any{"type": "string", "description": "menneskelig frekvens, f.eks. «hver dag kl 08:00»"},
						"interval_seconds":  map[string]any{"type": "integer", "description": "frekvens i sekunder (min 900)"},
						"run_time":          map[string]any{"type": "string", "description": "HH:MM ved dag/uke-intervall"},
						"daily_token_limit": map[string]any{"type": "integer"},
						"write_access":      map[string]any{"type": "boolean"},
					},
					"required": []string{"agent_id"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "delete_agent",
				"description": "Slett agenten. Bekreft med brukeren først.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agent_id": map[string]any{"type": "string"},
					},
					"required": []string{"agent_id"},
				},
			},
		},
	}
}

// runUpdateAgent oppdaterer en agent fra et modell-verktøykall (kun oppgitte felt).
func (s *Server) runUpdateAgent(ctx context.Context, a agentToolArgs) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	cur, err := s.store.GetAgent(a.AgentID, user.ID)
	if err != nil {
		return "Fant ikke agenten."
	}
	if strings.TrimSpace(a.Name) != "" {
		cur.Name = strings.TrimSpace(a.Name)
	}
	if strings.TrimSpace(a.Task) != "" {
		cur.Task = strings.TrimSpace(a.Task)
	}
	if strings.TrimSpace(a.ScheduleLabel) != "" {
		cur.ScheduleLabel = strings.TrimSpace(a.ScheduleLabel)
	}
	if a.IntervalSeconds > 0 {
		if a.IntervalSeconds < 900 {
			a.IntervalSeconds = 900
		}
		cur.IntervalSeconds = a.IntervalSeconds
	}
	if strings.TrimSpace(a.RunTime) != "" {
		cur.RunTime = strings.TrimSpace(a.RunTime)
	}
	if a.DailyTokenLimit > 0 {
		cur.DailyTokenLimit = a.DailyTokenLimit
	}
	if err := s.store.UpdateAgentConfig(a.AgentID, user.ID, cur); err != nil {
		return "Kunne ikke oppdatere agenten."
	}
	return "Agent oppdatert."
}

// buildAgentTools lager verktøyene modellen bruker til å administrere agenter.
// Kun tilgjengelig i /agent-modus, så vanlig chat ikke forurenses.
func (s *Server) buildAgentTools(ctx context.Context) []any {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return nil
	}

	tools := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "list_agents",
				"description": "List brukerens agenter. Vis alltid resultatet til brukeren som en ```agents kodeblokk med JSON-en verktøyet returnerer.",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
	}

	// Tilkoblinger modellen kan referere i create_agent.
	var connLines strings.Builder
	conns, _ := s.store.ListConnections(user.TenantID)
	for _, c := range conns {
		fmt.Fprintf(&connLines, "- id=%s navn=%q (%s)\n", c.ID, c.Name, c.Driver)
	}
	if connLines.Len() == 0 {
		connLines.WriteString("(ingen tilkoblinger tilgjengelig)\n")
	}

	tools = append(tools,
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "create_agent",
				"description": "Opprett en agent når alt er bekreftet med brukeren. Tilgjengelige tilkoblinger:\n" +
					connLines.String(),
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":              map[string]any{"type": "string", "description": "kort navn på agenten"},
						"task":              map[string]any{"type": "string", "description": "hva agenten skal gjøre, som en instruks"},
						"connection_id":     map[string]any{"type": "string", "description": "id for tilkoblingen agenten jobber mot"},
						"schedule_label":    map[string]any{"type": "string", "description": "menneskelig frekvens, f.eks. «hver dag kl 08:00»"},
						"interval_seconds":  map[string]any{"type": "integer", "description": "frekvens i sekunder (86400 = daglig)"},
						"daily_token_limit": map[string]any{"type": "integer", "description": "maks tokens agenten kan bruke per dag"},
						"write_access":      map[string]any{"type": "boolean", "description": "true kun hvis brukeren eksplisitt ba om skrivetilgang"},
					},
					"required": []string{"name", "task", "connection_id", "schedule_label", "interval_seconds", "daily_token_limit"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "delete_agent",
				"description": "Slett en agent brukeren eier. Bekreft med brukeren først.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agent_id": map[string]any{"type": "string", "description": "id for agenten som skal slettes"},
					},
					"required": []string{"agent_id"},
				},
			},
		},
	)
	return tools
}

// agentToolArgs er feltene agent-verktøyene tar imot.
type agentToolArgs struct {
	Name            string `json:"name"`
	Task            string `json:"task"`
	ConnectionID    string `json:"connection_id"`
	ScheduleLabel   string `json:"schedule_label"`
	IntervalSeconds int    `json:"interval_seconds"`
	RunTime         string `json:"run_time"`
	DailyTokenLimit int    `json:"daily_token_limit"`
	WriteAccess     bool   `json:"write_access"`
	AgentID         string `json:"agent_id"`
}

// runCreateAgent oppretter en agent fra et modell-verktøykall.
func (s *Server) runCreateAgent(ctx context.Context, a agentToolArgs) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Task) == "" {
		return "Mangler navn eller oppgave."
	}
	// Valider at tilkoblingen tilhører tenanten.
	conns, err := s.store.ListConnections(user.TenantID)
	if err != nil {
		return "Kunne ikke slå opp tilkoblinger."
	}
	valid := false
	for _, c := range conns {
		if c.ID == a.ConnectionID {
			valid = true
			break
		}
	}
	if !valid {
		return "Ukjent connection_id — bruk en av de listede tilkoblingene."
	}
	if a.IntervalSeconds <= 0 {
		a.IntervalSeconds = 86400
	} else if a.IntervalSeconds < 900 {
		a.IntervalSeconds = 900 // minimum 15 min
	}
	agent, err := s.store.CreateAgent(user.TenantID, user.ID, store.Agent{
		Name:            strings.TrimSpace(a.Name),
		Task:            strings.TrimSpace(a.Task),
		ConnectionID:    a.ConnectionID,
		ScheduleLabel:   strings.TrimSpace(a.ScheduleLabel),
		IntervalSeconds: a.IntervalSeconds,
		DailyTokenLimit: a.DailyTokenLimit,
		WriteAccess:     a.WriteAccess,
	})
	if err != nil {
		s.log.Error("kunne ikke opprette agent", "err", err)
		return "Kunne ikke opprette agenten."
	}
	s.log.Info("agent opprettet", "id", agent.ID, "navn", agent.Name)
	out, _ := json.Marshal(agent)
	return "Agent opprettet:\n" + string(out)
}

// runListAgents returnerer brukerens agenter som JSON.
func (s *Server) runListAgents(ctx context.Context) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	agents, err := s.store.ListAgents(user.ID)
	if err != nil {
		return "Kunne ikke hente agenter."
	}
	if agents == nil {
		agents = []store.Agent{}
	}
	out, _ := json.Marshal(map[string]any{"agents": agents})
	return string(out)
}

// runDeleteAgent sletter en agent brukeren eier.
func (s *Server) runDeleteAgent(ctx context.Context, a agentToolArgs) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	if err := s.store.DeleteAgent(a.AgentID, user.ID); err != nil {
		return "Fant ikke agenten."
	}
	return "Agent slettet."
}
