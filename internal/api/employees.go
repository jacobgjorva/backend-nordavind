package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// handleListEmployees returnerer tenantens ansatt-register.
func (s *Server) handleListEmployees(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	emps, err := s.store.ListEmployees(user.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "intern feil")
		return
	}
	if emps == nil {
		emps = []store.Employee{}
	}
	writeJSON(w, map[string]any{"employees": emps})
}

type employeeInput struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description"`
	Email       string `json:"email"`
}

func (in employeeInput) toEmployee() store.Employee {
	return store.Employee{
		Name:        strings.TrimSpace(in.Name),
		Role:        strings.TrimSpace(in.Role),
		Description: strings.TrimSpace(in.Description),
		Email:       strings.TrimSpace(in.Email),
	}
}

// handleCreateEmployee legger til en ansatt.
func (s *Server) handleCreateEmployee(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var in employeeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "ugyldig request")
		return
	}
	e := in.toEmployee()
	if e.Name == "" {
		writeErr(w, http.StatusBadRequest, "navn må være satt")
		return
	}
	saved, err := s.store.CreateEmployee(user.TenantID, e)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "kunne ikke lagre")
		return
	}
	writeJSON(w, saved)
}

// handleUpdateEmployee endrer en ansatt.
func (s *Server) handleUpdateEmployee(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var in employeeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "ugyldig request")
		return
	}
	e := in.toEmployee()
	if e.Name == "" {
		writeErr(w, http.StatusBadRequest, "navn må være satt")
		return
	}
	err := s.store.UpdateEmployee(r.PathValue("id"), user.TenantID, e)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "ikke funnet")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "intern feil")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteEmployee fjerner en ansatt.
func (s *Server) handleDeleteEmployee(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteEmployee(r.PathValue("id"), user.TenantID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "ikke funnet")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "intern feil")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
