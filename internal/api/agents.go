package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// handleCreateAgent oppretter en agent fra config-widgeten i chatten.
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" {
		http.Error(w, "mangler oppgave", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = "Agent"
	}
	// Tilkobling er valgfri (f.eks. websøk-agent). Hvis satt, må den
	// tilhøre tenanten.
	if req.ConnectionID != "" {
		conns, err := s.store.ListConnections(user.TenantID)
		if err != nil {
			http.Error(w, "intern feil", http.StatusInternalServerError)
			return
		}
		valid := false
		for _, c := range conns {
			if c.ID == req.ConnectionID {
				valid = true
				break
			}
		}
		if !valid {
			http.Error(w, "ukjent tilkobling", http.StatusBadRequest)
			return
		}
	}
	if req.IntervalSeconds <= 0 {
		req.IntervalSeconds = 86400
	} else if req.IntervalSeconds < 900 {
		req.IntervalSeconds = 900 // minimum 15 min
	}

	agent, err := s.store.CreateAgent(user.TenantID, user.ID, store.Agent{
		Name:            req.Name,
		Task:            req.Task,
		ConnectionID:    req.ConnectionID,
		ScheduleLabel:   strings.TrimSpace(req.ScheduleLabel),
		IntervalSeconds: req.IntervalSeconds,
		RunTime:         strings.TrimSpace(req.RunTime),
		DailyTokenLimit: req.DailyTokenLimit,
		WriteAccess:     req.WriteAccess,
	})
	if err != nil {
		s.log.Error("kunne ikke opprette agent", "err", err)
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	s.log.Info("agent opprettet", "id", agent.ID, "navn", agent.Name)
	// Spinup: finn beste fremgangsmåte én gang, i bakgrunnen, før første kjøring.
	s.startPlanBuild(agent.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agent)
}

// handleCreateDraftAgent oppretter en tom agent-chat brukeren lander i når hen
// skriver /agent. Agenten er deaktivert (kjører ikke) til den er konfigurert.
func (s *Server) handleCreateDraftAgent(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	agent, err := s.store.CreateAgent(user.TenantID, user.ID, store.Agent{
		Name:            "Ny agent",
		Task:            "",
		IntervalSeconds: 86400,
	})
	if err != nil {
		s.log.Error("kunne ikke opprette draft-agent", "err", err)
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	// Draft skal ikke kjøre før brukeren har konfigurert og aktivert den.
	if err := s.store.SetAgentEnabled(agent.ID, user.ID, false); err != nil {
		s.log.Error("kunne ikke deaktivere draft-agent", "err", err)
	}
	agent.Enabled = false

	// Agenten åpner alltid med å spørre hva den skal gjøre.
	if err := s.store.AppendAgentMessage(agent.ChatID, store.ChatMessage{
		Role: "assistant", Content: "Ny agent her. Hva skal jeg gjøre?",
	}); err != nil {
		s.log.Error("kunne ikke seed'e draft-chat", "err", err)
	}
	s.log.Info("draft-agent opprettet", "id", agent.ID, "chat", agent.ChatID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agent)
}

// handleAgentConnections lister tenantens tilkoblinger (id + navn) som
// agent-widgeten lar brukeren velge mellom. Trygt for alle innloggede.
func (s *Server) handleAgentConnections(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	conns, err := s.store.ListConnections(user.TenantID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]string, 0, len(conns))
	for _, c := range conns {
		out = append(out, map[string]string{"id": c.ID, "name": c.Name, "driver": c.Driver})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"connections": out})
}

// agentState avleder live-tilstanden trollet viser i farmen. Prioritet:
// pågående arbeid slår plan-status, plan-status slår hvile.
func (s *Server) agentState(a store.Agent) string {
	s.runMu.Lock()
	running := s.runActive[a.ID]
	s.runMu.Unlock()
	if running {
		return "working"
	}
	s.planMu.Lock()
	building := s.planBuilding[a.ID]
	s.planMu.Unlock()
	if building {
		return "thinking"
	}
	switch {
	case a.PlanStatus == "broken":
		return "broken"
	case !a.Enabled:
		return "paused"
	default:
		return "sleeping"
	}
}

// agentWithState er en agent + live-tilstand, til farmen og agent-widgetene.
type agentWithState struct {
	store.Agent
	State string `json:"state"`
}

// handleListAgents returnerer brukerens agenter med live-tilstand.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	agents, err := s.store.ListAgents(user.ID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	out := make([]agentWithState, 0, len(agents))
	for _, a := range agents {
		out = append(out, agentWithState{Agent: a, State: s.agentState(a)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"agents": out})
}

// handleAgentRuns returnerer kjøringshistorikken til trådgrafen.
// ?hours styrer vinduet (default 24, maks 168).
func (s *Server) handleAgentRuns(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	hours := 24
	if h, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && h > 0 && h <= 168 {
		hours = h
	}
	runs, err := s.store.AgentRunsSince(user.ID, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []store.AgentRunEvent{}
	}
	writeJSON(w, map[string]any{"runs": runs})
}

// handleMarkAgentSeen kvitterer ut et ulest svar — kalles når brukeren
// ekspanderer funn-pillen i grafen (eller åpner chatten, som også kvitterer).
func (s *Server) handleMarkAgentSeen(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	if err := s.store.MarkAgentResponseSeen(r.PathValue("id"), user.ID); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetAgentPersona setter navn og/eller personlighet fra farmen.
func (s *Server) handleSetAgentPersona(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Name        string  `json:"name"`
		Personality *string `json:"personality"` // nil = ikke endre; tom streng = fjern
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	err := s.store.SetAgentPersona(r.PathValue("id"), user.ID, store.AgentPersona{
		Name: req.Name, Personality: req.Personality,
	})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentByChat returnerer agenten som eier en chat (for pause-knappen).
func (s *Server) handleAgentByChat(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	agent, err := s.store.AgentByChat(r.PathValue("chatId"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	// Å åpne agent-chatten kvitterer ut et ulest svar (grafen flytter noden
	// tilbake fra svar-seksjonen).
	if err := s.store.MarkAgentResponseSeen(agent.ID, user.ID); err != nil {
		s.log.Warn("kunne ikke markere svar som sett", "id", agent.ID, "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agent)
}

// handleSetAgentEnabled oppdaterer pause- og/eller push-status. Feltene er
// valgfrie (pekere), så bare det som sendes endres.
func (s *Server) handleSetAgentEnabled(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled     *bool `json:"enabled"`
		PushEnabled *bool `json:"push_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if req.Enabled != nil {
		if err := s.setAgentField(w, s.store.SetAgentEnabled(id, user.ID, *req.Enabled)); err != nil {
			return
		}
	}
	if req.PushEnabled != nil {
		if err := s.setAgentField(w, s.store.SetAgentPush(id, user.ID, *req.PushEnabled)); err != nil {
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// setAgentField mapper en store-feil til riktig HTTP-svar; returnerer feilen
// (ikke-nil) hvis kalleren skal stoppe.
func (s *Server) setAgentField(w http.ResponseWriter, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return err
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return err
	}
	return nil
}

// handleUpdateAgent oppdaterer en agents konfigurasjon (redigering i chatten).
func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Name            string `json:"name"`
		Task            string `json:"task"`
		ConnectionID    string `json:"connection_id"`
		ScheduleLabel   string `json:"schedule_label"`
		IntervalSeconds int    `json:"interval_seconds"`
		RunTime         string `json:"run_time"`
		DailyTokenLimit int    `json:"daily_token_limit"`
		WriteAccess     bool   `json:"write_access"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" {
		http.Error(w, "mangler oppgave", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = "Agent"
	}
	if req.ConnectionID != "" {
		conns, _ := s.store.ListConnections(user.TenantID)
		valid := false
		for _, c := range conns {
			if c.ID == req.ConnectionID {
				valid = true
				break
			}
		}
		if !valid {
			http.Error(w, "ukjent tilkobling", http.StatusBadRequest)
			return
		}
	}
	if req.IntervalSeconds <= 0 {
		req.IntervalSeconds = 86400
	} else if req.IntervalSeconds < 900 {
		req.IntervalSeconds = 900 // minimum 15 min
	}
	// Endres oppgaven, er den gamle planen ikke lenger gyldig — den ble
	// kompilert for noe annet. Bygg en ny.
	id := r.PathValue("id")
	prev, _ := s.store.GetAgent(id, user.ID)
	taskChanged := strings.TrimSpace(prev.Task) != req.Task

	err := s.store.UpdateAgentConfig(id, user.ID, store.Agent{
		Name:            req.Name,
		Task:            req.Task,
		ConnectionID:    req.ConnectionID,
		ScheduleLabel:   strings.TrimSpace(req.ScheduleLabel),
		IntervalSeconds: req.IntervalSeconds,
		RunTime:         strings.TrimSpace(req.RunTime),
		DailyTokenLimit: req.DailyTokenLimit,
		WriteAccess:     req.WriteAccess,
	})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if taskChanged {
		s.store.ClearAgentPlan(id)
		s.startPlanBuild(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAgent sletter en agent brukeren eier.
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	// Grafen først — etter sletting finnes ikke planen som peker på den.
	s.deleteAgentChart(r.PathValue("id"), user.ID)
	err := s.store.DeleteAgent(r.PathValue("id"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
