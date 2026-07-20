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
		"Eskalering til en person: prøv ALLTID å løse oppgaven selv først (kunnskap og verktøy). KUN hvis " +
			"du etter å ha forsøkt er sikker på at du ikke kommer videre (mangler data, verktøy eller " +
			"tilgang) OG én bestemt person i registeret åpenbart kan, kaller du verktøyet contact_person " +
			"med personens navn/e-post fra registeret og et kort utkast skrevet i FØRSTEPERSON som brukeren " +
			"selv (mailen sendes fra brukerens konto, ikke fra deg). Det viser et ferdig e-postkort brukeren " +
			"selv sender. Velg ÉN person. For alt du kan svare på eller løse selv: svar normalt og nevn " +
			"aldri registeret.",
	)
	return b.String()
}

// hasEmployees er sann hvis tenanten har minst én ansatt i registeret.
func (s *Server) hasEmployees(tenantID string) bool {
	emps, err := s.store.ListEmployees(tenantID)
	return err == nil && len(emps) > 0
}

// contactTool lar modellen eskalere til en person deterministisk: den kaller
// verktøyet med mottaker + utkast, og backend rendrer compose-kortet. Mye mer
// robust enn å be modellen skrive en fri-tekst-blokk.
var contactTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "contact_person",
		"description": "Bruk KUN når du etter å ha forsøkt selv (kunnskap/verktøy) er sikker på at du " +
			"ikke kommer videre, og én bestemt person i ansatt-registeret åpenbart kan hjelpe. Da lager " +
			"dette et ferdig e-postutkast til personen som brukeren kan redigere og sende. Ikke bruk det " +
			"for noe du kan svare på eller løse selv.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "personens navn fra registeret"},
				"email":   map[string]any{"type": "string", "description": "personens e-postadresse fra registeret"},
				"subject": map[string]any{"type": "string", "description": "kort emne"},
				"body":    map[string]any{"type": "string", "description": "utkast til meldingen, på norsk, uten signatur. Skriv i FØRSTEPERSON som brukeren selv (mailen sendes fra brukerens konto), f.eks. «Hei <navn>, jeg har et problem med ... kan du hjelpe?» — aldri i tredjeperson om «brukeren»"},
			},
			"required": []string{"email", "subject", "body"},
		},
	},
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
