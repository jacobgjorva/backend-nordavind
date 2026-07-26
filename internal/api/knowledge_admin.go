package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// handleListPending returnerer noder som venter på godkjenning.
func (s *Server) handleListPending(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	nodes, err := s.store.PendingNodes(user.TenantID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if nodes == nil {
		nodes = []store.KnowledgeNode{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
}

// handleKnowledgeGraph returnerer aksepterte noder + kanter til visualisering.
func (s *Server) handleKnowledgeGraph(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	nodes, edges, err := s.store.GraphData(user.TenantID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if nodes == nil {
		nodes = []store.KnowledgeNode{}
	}
	if edges == nil {
		edges = []store.KnowledgeEdge{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"nodes": nodes, "edges": edges})
}

// handleCreateEdge kobler to noder i graf-editoren.
func (s *Server) handleCreateEdge(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var e store.KnowledgeEdge
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.FromID == "" || e.ToID == "" || e.FromID == e.ToID {
		http.Error(w, "ugyldig kant", http.StatusBadRequest)
		return
	}
	if e.Relation == "" {
		e.Relation = "relatert til"
	}
	if err := s.store.AddEdge(user.TenantID, e); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteEdge fjerner en kant i graf-editoren.
func (s *Server) handleDeleteEdge(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var e store.KnowledgeEdge
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.FromID == "" || e.ToID == "" {
		http.Error(w, "ugyldig kant", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteEdge(user.TenantID, e); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAcceptNode godkjenner en node (med evt. redigert tekst) og reberegner
// embedding hvis teksten er endret.
func (s *Server) handleAcceptNode(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		// Dublettvakten (G5): replace_id = erstatt den lignende lappen;
		// keep_both = godkjenn likevel. Uten noen av dem stoppes godkjenning
		// av en nær-dublett med 409 + kandidaten — aldri auto.
		ReplaceID string `json:"replace_id"`
		KeepBoth  bool   `json:"keep_both"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Summary = strings.TrimSpace(req.Summary)
	if req.Title == "" || req.Summary == "" {
		http.Error(w, "mangler tittel eller summary", http.StatusBadRequest)
		return
	}
	vec, err := s.embed(r.Context(), req.Title+". "+req.Summary)
	if err != nil {
		http.Error(w, "kunne ikke lage embedding", http.StatusBadGateway)
		return
	}
	// Dublettvakt: målt på ekte aksepterte fakta ligger DISTINKTE par under
	// 0.58 cosine — 0.75 flagger kun ekte omformuleringer av samme faktum.
	const dupThreshold = 0.75
	if req.ReplaceID == "" && !req.KeepBoth {
		if dup, sim, derr := s.store.MostSimilarAcceptedFact(user.TenantID, vec, r.PathValue("id")); derr == nil && sim >= dupThreshold {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{
				"duplicate": map[string]any{"id": dup.ID, "title": dup.Title, "text": dup.Text, "similarity": sim},
			})
			return
		}
	}
	if req.ReplaceID != "" {
		// Erstatning: den gamle noden+lappen fjernes før den nye godkjennes.
		if err := s.store.DeleteNode(req.ReplaceID, user.TenantID); err != nil && !errors.Is(err, store.ErrNotFound) {
			http.Error(w, "kunne ikke erstatte", http.StatusInternalServerError)
			return
		}
	}
	err = s.store.AcceptNode(r.PathValue("id"), user.TenantID, req.Title, req.Summary, vec)
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

// handleUpdateNode redigerer en akseptert node manuelt (reberegner embedding).
func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Summary = strings.TrimSpace(req.Summary)
	if req.Title == "" || req.Summary == "" {
		http.Error(w, "mangler tittel eller summary", http.StatusBadRequest)
		return
	}
	vec, err := s.embed(r.Context(), req.Title+". "+req.Summary)
	if err != nil {
		http.Error(w, "kunne ikke lage embedding", http.StatusBadGateway)
		return
	}
	err = s.store.UpdateNode(r.PathValue("id"), user.TenantID, req.Title, req.Summary, vec)
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

// handleDeleteNode sletter en node.
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteNode(r.PathValue("id"), user.TenantID)
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

// handleRejectNode avviser en node.
func (s *Server) handleRejectNode(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	err := s.store.RejectNode(r.PathValue("id"), user.TenantID)
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
