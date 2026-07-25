package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
		Mission:         req.Mission,
		SendMail:        req.SendMail,
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

// handleSetMissionPlan lagrer mål, fullført-kriterier og token-tak for et
// oppdrag. Kriteriene må godkjennes (egen rute) før løkka starter.
func (s *Server) handleSetMissionPlan(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Goal     string `json:"goal"`
		Criteria string `json:"criteria"`
		Budget   int    `json:"budget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Goal) == "" || strings.TrimSpace(req.Criteria) == "" {
		http.Error(w, "mål og kriterier må være satt", http.StatusBadRequest)
		return
	}
	err := s.store.SetMissionPlan(r.PathValue("id"), user.ID,
		strings.TrimSpace(req.Goal), strings.TrimSpace(req.Criteria), req.Budget)
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

// handleApproveMission godkjenner kriteriene og starter den kontinuerlige løkka.
func (s *Server) handleApproveMission(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := s.store.ApproveMission(id, user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	s.startMissionRunner(context.Background(), id)
	w.WriteHeader(http.StatusNoContent)
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

// handleListAgents returnerer brukerens agenter.
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
	if agents == nil {
		agents = []store.Agent{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"agents": agents})
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
