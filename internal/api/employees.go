package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// employeeContext bygger ansatt-registeret + eskaleringsinstruksen som
// injiseres i chat-systemet. Tom streng hvis registeret er tomt (null overhead).
// KUN når modellen er sikker på at den ikke kommer videre selv skal den foreslå
// å kontakte en person og drafte en mailcompose-blokk ved bekreftelse.
func (s *Server) employeeContext(ctx context.Context, tenantID string) string {
	emps, err := s.store.ListEmployees(tenantID)
	if err != nil || len(emps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Internt ansatt-register (hvem gjør hva):\n")
	for _, e := range emps {
		fmt.Fprintf(&b, "- %s", e.Name)
		if e.Role != "" {
			fmt.Fprintf(&b, ", %s", e.Role)
		}
		if e.Description != "" {
			fmt.Fprintf(&b, " — %s", e.Description)
		}
		if e.Email != "" {
			fmt.Fprintf(&b, " <%s>", e.Email)
		}
		b.WriteString("\n")
	}
	b.WriteString(
		"Hvis — og KUN hvis — du er helt sikker på at du ikke kan fullføre det brukeren ber om " +
			"selv (mangler data, verktøy eller tilgang) og en person i registeret åpenbart kan, foreslå " +
			"å kontakte dem med ÉN kort setning: «Skal jeg sende en mail til <navn> for å be om det jeg " +
			"trenger?». Gjør dette ALDRI for noe du kan svare på eller løse selv, og nevn ellers aldri " +
			"registeret. Når brukeren bekrefter, svar KUN med en ```mailcompose kodeblokk med JSON: " +
			"{\"to\":[{\"name\":\"<navn>\",\"address\":\"<e-post>\"}],\"subject\":\"<emne>\",\"body\":\"<utkast, norsk>\"}.",
	)
	return b.String()
}

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
