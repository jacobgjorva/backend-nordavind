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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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

// handleAcceptNode godkjenner en node (med evt. redigert tekst) og reberegner
// embedding hvis teksten er endret.
func (s *Server) handleAcceptNode(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
