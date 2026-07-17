package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// requireAdmin krever innlogget bruker med admin-rolle.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(userKey).(store.User)
		if !ok || user.Role != "admin" {
			http.Error(w, "krever admin", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// handleAdminListUsers returnerer tenantens brukere med forbruk siste 30 dager.
func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value(userKey).(store.User)

	users, err := s.store.ListUsers(admin.TenantID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	usage, err := s.store.PerUserUsage(admin.TenantID, 30)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}

	type row struct {
		store.User
		Usage store.UserUsage `json:"usage"`
	}
	out := make([]row, 0, len(users))
	for _, u := range users {
		out = append(out, row{User: u, Usage: usage[u.ID]})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"users": out})
}

// handleAdminCreateUser oppretter en bruker i admins egen tenant.
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value(userKey).(store.User)

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		http.Error(w, "ugyldig e-post", http.StatusBadRequest)
		return
	}
	role := req.Role
	if role != "admin" {
		role = "member"
	}

	user, err := s.store.CreateUser(admin.TenantID, email, role)
	if err != nil {
		http.Error(w, "kunne ikke opprette bruker (finnes den fra før?)", http.StatusConflict)
		return
	}
	s.log.Info("bruker opprettet", "email", email, "av", admin.Email)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// handleAdminDeleteUser fjerner en bruker fra admins tenant.
func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value(userKey).(store.User)
	userID := r.PathValue("id")

	if userID == admin.ID {
		http.Error(w, "kan ikke slette deg selv", http.StatusBadRequest)
		return
	}
	err := s.store.DeleteUser(admin.TenantID, userID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	s.log.Info("bruker slettet", "user_id", userID, "av", admin.Email)
	w.WriteHeader(http.StatusNoContent)
}
