package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

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
	// Hjernen tegnes i SAMME graf: entiteter blir noder, påstander mellom to
	// entiteter blir kanter, og påstander med en ren verdi («48 timer før
	// levering») legges på entitetens linje. Uten dette er hjernen usynlig
	// for den som skal rette den.
	if s.cfg.BrainMode == "on" {
		bn, be := s.brainGraph(user.TenantID)
		nodes = append(nodes, bn...)
		edges = append(edges, be...)
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
