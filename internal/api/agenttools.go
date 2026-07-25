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
const agentSetupSystem = "Du er i OPPSETT-modus. Du skal KUN sette opp agenten, ALDRI utføre selve oppgaven nå: " +
	"ikke søk på nettet, ikke spør databasen, ikke send mail, ikke be om tilgang, ikke løs problemet selv. " +
	"Mangler du noe for å sette opp agenten godt, SPØR brukeren. Selve jobben kjøres av agenten etterpå.\n\n" +
	"Brukeren vil sette opp en agent. En agent er enten en RUTINE (samme sjekk på fast " +
	"frekvens, f.eks. «følg med på X hver dag») eller et OPPDRAG (en større oppgave agenten jobber mot til " +
	"målet er nådd og så gir seg, f.eks. «finn de neste 3 kundene jeg bør jakte på»). " +
	"FØRSTE svar: ÉN kort setning som spør hva agenten skal gjøre («Hva skal agenten gjøre for deg?»). " +
	"Ut fra svaret avgjør du selv om det er rutine eller oppdrag, og om det trenger database eller websøk.\n" +
	"\nMÅLET med oppsettet er at agenten skal LYKKES best mulig. Ikke hast videre — still oppfølgings-" +
	"spørsmål, ETT om gangen, til du har alt agenten trenger. Spør bare om det som faktisk er relevant for " +
	"nettopp denne oppgaven, og hopp over resten.\n" +
	"For et OPPDRAG, grav i det som avgjør resultatet, f.eks.: hva som gjør oppdraget vellykket " +
	"(konkret suksesskriterium), omfang/avgrensning, hvilke datakilder/tilkobling som gjelder, relevante " +
	"kriterier eller preferanser, hvem som eventuelt skal kontaktes, og om agenten selv skal få sende mail " +
	"eller kun foreslå den. Frekvens er som regel IKKE relevant for et oppdrag — ikke spør om det; oppdraget " +
	"starter og jobber til det er ferdig.\n" +
	"For en RUTINE, spør om hvor ofte og på hvilket klokkeslett. MINIMUM intervall 15 min (900 sek); ved " +
	"hyppigere ønske, forklar kort at 15 min er minste og foreslå det.\n" +
	"For database-oppgaver: spør hvilken tilkobling (bruk navnet, aldri finn opp en id). Websøk trenger ingen.\n" +
	"Når du har alt: én kort bekreftelse, så en ```agent_setup kodeblokk med JSON:\n" +
	"{\"name\":\"kort navn\",\"task\":\"presis, fyldig instruks som fanger alt brukeren oppga (mål, " +
	"suksesskriterium, avgrensning, kriterier)\",\"fields\":[ ... ]}\n" +
	"task skal være rik nok til at agenten kan lykkes uten å spørre igjen — bak inn detaljene fra samtalen.\n" +
	"fields = kontrollene widgeten skal vise, KUN relevante, hver med fornuftig forhåndsverdi:\n" +
	"- {\"type\":\"interval\",\"value\":<sekunder; 3600=time, 86400=dag, 172800=annenhver dag, 604800=uke>} " +
	"(RUTINE: fast kadens. OPPDRAG: sett 900 så den starter snart og jobber til ferdig)\n" +
	"- {\"type\":\"time\",\"value\":\"HH:MM\"} (kun rutiner med intervall minst én dag)\n" +
	"- {\"type\":\"effort\",\"value\":\"Lav|Moderat|Høy|Max\"}\n" +
	"- {\"type\":\"connection\",\"value\":\"<connection_id>\"} (KUN database-agenter)\n" +
	"- {\"type\":\"write\",\"value\":false} (KUN database-agenter som kan trenge skriving)\n" +
	"- {\"type\":\"mission\",\"value\":true} (KUN for oppdrag)\n" +
	"- {\"type\":\"send_mail\",\"value\":false} (KUN oppdrag: true hvis brukeren uttrykkelig lot agenten " +
	"sende mail selv)\n" +
	"Ta alltid med interval og effort. Utelat connection og write for websøk. For oppdrag: ta med " +
	"mission=true og interval=900, og send_mail etter avklaring. Ikke skriv noe etter kodeblokken. " +
	"Hvis brukeren vil se agentene sine, kall list_agents og gjengi som en ```agents kodeblokk."

// missionPlanSystem styrer en fersk agent-chat. Agenten er en arbeidsmaskin:
// forstår oppgaven, utleder selv fullført-kriterier, og starter løkka umiddelbart
// via start_mission — uten å ekko målet, uten kort, uten å nevne token-tak.
const missionPlanSystem = "Du setter opp en gjentakende agent-rutine for brukeren. Når brukeren beskriver " +
	"behovet: kall setup_routine med prompt (hva som gjøres HVER kjøring), interval_seconds (minimum 900; " +
	"time=3600, dag=86400, uke=604800) og en kort label («hvert 15. minutt», «hver morgen kl 08»). " +
	"Beskriver brukeren noe uten frekvens: foreslå en fornuftig frekvens i prompten din, eller spør kort. " +
	"Svar KUN med en kort kvittering («Rutine opprettet — kjører <frekvens>.») — ingenting mer. " +
	"IKKE utfør selve oppgaven her (ikke søk/spør database) — rutinen gjør jobben på hver kjøring."

// missionStartTools gir planleggeren to verktøy: start et engangsoppdrag,
// eller sett opp en gjentakende rutine når behovet aldri «fullføres».
func missionStartTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "setup_routine",
				"description": "Gjør agenten til en gjentakende rutine som kjører prompten på fast intervall. Bruk for alt som skal skje «hvert/hver …» — rutiner fullføres aldri.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"prompt":           map[string]any{"type": "string", "description": "hva agenten skal gjøre hver kjøring"},
						"interval_seconds": map[string]any{"type": "integer", "description": "sekunder mellom kjøringer, minimum 900"},
						"label":            map[string]any{"type": "string", "description": "menneskelig frekvens, f.eks. «hvert 15. minutt»"},
						"name":             map[string]any{"type": "string", "description": "kort agentnavn"},
					},
					"required": []string{"prompt", "interval_seconds"},
				},
			},
		},
	}
}

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
	taskChanged := false
	if t := strings.TrimSpace(a.Task); t != "" && t != strings.TrimSpace(cur.Task) {
		cur.Task = t
		taskChanged = true
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
	// Ny oppgave = den gamle planen gjelder ikke lenger.
	if taskChanged {
		s.store.ClearAgentPlan(a.AgentID)
		s.startPlanBuild(a.AgentID)
	}
	return "Agent oppdatert."
}

// runStartMission setter oppdraget (ubegrenset token-tak), godkjenner det og
// starter den kontinuerlige løkka fra et start_mission-verktøykall.
func (s *Server) runStartMission(ctx context.Context, agentID, goal, criteria string) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(goal) == "" {
		return "Mangler oppdrag."
	}
	if strings.TrimSpace(criteria) == "" {
		criteria = "Oppgaven i målet er utført og resultatet er levert."
	}
	if err := s.store.SetMissionPlan(agentID, user.ID, goal, criteria, 0); err != nil {
		return "Kunne ikke sette oppdraget."
	}
	if err := s.store.ApproveMission(agentID, user.ID); err != nil {
		return "Kunne ikke starte oppdraget."
	}
	s.startMissionRunner(context.Background(), agentID)
	return "startet"
}

// runSetupRoutine gjør utkast-agenten til en gjentakende rutine: prompt +
// intervall, aktivert, ingen oppdrags-løkke. Rutiner «fullføres» aldri.
func (s *Server) runSetupRoutine(ctx context.Context, agentID, rawArgs string) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	var args struct {
		Prompt          string `json:"prompt"`
		IntervalSeconds int    `json:"interval_seconds"`
		Label           string `json:"label"`
		Name            string `json:"name"`
	}
	json.Unmarshal([]byte(rawArgs), &args)
	if strings.TrimSpace(args.Prompt) == "" {
		return "Mangler prompt."
	}
	if args.IntervalSeconds < 900 {
		args.IntervalSeconds = 900
	}
	// Uten agent-kontekst (create_routine-flyten i vanlig chat): opprett en ny
	// rutine direkte — før feilet dette med «Fant ikke agenten».
	if strings.TrimSpace(agentID) == "" {
		name := strings.TrimSpace(args.Name)
		if name == "" {
			name = strings.TrimSpace(args.Label)
		}
		if name == "" {
			name = "Rutine"
		}
		agent, err := s.store.CreateAgent(user.TenantID, user.ID, store.Agent{
			Name:            name,
			Task:            args.Prompt,
			ScheduleLabel:   strings.TrimSpace(args.Label),
			IntervalSeconds: args.IntervalSeconds,
		})
		if err != nil {
			s.log.Error("kunne ikke opprette rutine fra chat", "err", err)
			return "Kunne ikke opprette rutinen."
		}
		s.log.Info("rutine opprettet fra chat", "id", agent.ID, "navn", agent.Name)
		freq := strings.TrimSpace(args.Label)
		if freq == "" {
			freq = fmt.Sprintf("hvert %d. minutt", args.IntervalSeconds/60)
		}
		return "Rutine «" + agent.Name + "» opprettet — kjører " + freq + "."
	}
	a, err := s.store.GetAgent(agentID, user.ID)
	if err != nil {
		return "Fant ikke agenten."
	}
	a.Task = args.Prompt
	a.IntervalSeconds = args.IntervalSeconds
	if args.Label != "" {
		a.ScheduleLabel = args.Label
	}
	if args.Name != "" {
		a.Name = args.Name
	}
	if err := s.store.UpdateAgentConfig(agentID, user.ID, a); err != nil {
		return "Kunne ikke lagre rutinen."
	}
	if err := s.store.SetAgentEnabled(agentID, user.ID, true); err != nil {
		return "Kunne ikke aktivere rutinen."
	}
	// Spinup i bakgrunnen: finn beste fremgangsmåte før første kjøring.
	s.store.ClearAgentPlan(agentID)
	s.startPlanBuild(agentID)
	return "rutine opprettet"
}

// buildAgentSetupTools gir veiviseren KUN list_agents. Selve opprettelsen skjer
// deterministisk via ```agent_setup-blokken (Aktiver-kortet i frontend), ikke et
// verktøykall — det gir auto-binding av tilkobling og en tydelig bekreftelse.
func buildAgentSetupTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "list_agents",
				"description": "List brukerens agenter. Vis alltid resultatet som en ```agents kodeblokk med JSON-en verktøyet returnerer.",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
	}
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
						"mission":           map[string]any{"type": "boolean", "description": "true hvis dette er et oppdrag agenten skal jobbe mot til målet er nådd (ikke en fast rutine)"},
						"send_mail":         map[string]any{"type": "boolean", "description": "true kun hvis brukeren eksplisitt lot agenten sende mail selv; ellers foreslår den mail brukeren sender"},
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
	Mission         bool   `json:"mission"`
	SendMail        bool   `json:"send_mail"`
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
		Mission:         a.Mission,
		SendMail:        a.SendMail,
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
	// Grafen først — etter sletting finnes ikke planen som peker på den.
	s.deleteAgentChart(a.AgentID, user.ID)
	if err := s.store.DeleteAgent(a.AgentID, user.ID); err != nil {
		return "Fant ikke agenten."
	}
	return "Agent slettet."
}
