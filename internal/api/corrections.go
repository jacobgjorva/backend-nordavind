package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// handleLogCorrection lagrer en brukerkorrigering på et AI-svar.
func (s *Server) handleLogCorrection(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	var c store.Correction
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	c.AnswerContent = strings.TrimSpace(c.AnswerContent)
	c.Correction = strings.TrimSpace(c.Correction)
	if c.AnswerContent == "" || c.Correction == "" {
		http.Error(w, "mangler svar eller korrigering", http.StatusBadRequest)
		return
	}
	if err := s.store.LogCorrection(user.TenantID, user.ID, c); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
