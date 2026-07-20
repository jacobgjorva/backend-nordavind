package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// contactTool bygger eskalerings-verktøyet med ansatt-registeret bakt inn i
// beskrivelsen — samlet ett sted (sendt kun med verktøyene), ikke duplisert i
// system-prompten. Returnerer nil hvis registeret er tomt.
func (s *Server) contactTool(tenantID string) map[string]any {
	emps, err := s.store.ListEmployees(tenantID)
	if err != nil || len(emps) == 0 {
		return nil
	}
	var dir strings.Builder
	dir.WriteString("Ansatt-register (velg ÉN person, bruk e-posten nøyaktig):\n")
	for _, e := range emps {
		fmt.Fprintf(&dir, "- %s", e.Name)
		if e.Role != "" {
			fmt.Fprintf(&dir, ", %s", e.Role)
		}
		if e.Description != "" {
			fmt.Fprintf(&dir, " — %s", e.Description)
		}
		if e.Email != "" {
			fmt.Fprintf(&dir, " <%s>", e.Email)
		}
		dir.WriteString("\n")
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "contact_person",
			"description": "Eskalering til en person. Bruk KUN når du etter å ha forsøkt selv " +
				"(kunnskap/verktøy) er sikker på at du ikke kommer videre og én bestemt person under " +
				"åpenbart kan hjelpe; da vises et ferdig e-postkort brukeren selv sender. Bruk det ALDRI " +
				"for noe du kan svare på eller løse selv.\n" + dir.String(),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    map[string]any{"type": "string", "description": "personens navn fra registeret"},
					"email":   map[string]any{"type": "string", "description": "personens e-postadresse fra registeret"},
					"subject": map[string]any{"type": "string", "description": "kort emne"},
					"body":    map[string]any{"type": "string", "description": "utkast, norsk, uten signatur. Skriv i FØRSTEPERSON som brukeren selv (mailen sendes fra brukerens konto), f.eks. «Hei <navn>, jeg har et problem med ... kan du hjelpe?» — aldri i tredjeperson om «brukeren»"},
				},
				"required": []string{"email", "subject", "body"},
			},
		},
	}
}

// composeBlock bygger ```mailcompose-blokken frontend rendrer som send-kortet.
func composeBlock(name, email, subject, body string) string {
	spec, _ := json.Marshal(map[string]any{
		"to":      []map[string]string{{"name": name, "address": email}},
		"subject": subject,
		"body":    body,
	})
	return "```mailcompose\n" + string(spec) + "\n```"
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
