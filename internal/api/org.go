package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Org-API-et (KUNNSKAP-V2 del 1): enheter administreres av admin; ansattes
// enhet/bruker-kobling går via det eksisterende employees-API-et som nå
// bærer unit_id/user_id. Ingen chat-vei hit — org registreres strukturert.

func (s *Server) handleListUnits(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(store.User)
	units, err := s.store.ListUnits(user.TenantID)
	if err != nil {
		http.Error(w, "kunne ikke hente enheter", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"units": units})
}

func (s *Server) handleCreateUnit(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(store.User)
	var u store.OrgUnit
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil || u.Name == "" {
		http.Error(w, "enheten trenger et navn", http.StatusBadRequest)
		return
	}
	created, err := s.store.CreateUnit(user.TenantID, u)
	if err != nil {
		http.Error(w, "kunne ikke opprette enheten", http.StatusInternalServerError)
		return
	}
	writeJSON(w, created)
}

func (s *Server) handleUpdateUnit(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(store.User)
	var u store.OrgUnit
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil || u.Name == "" {
		http.Error(w, "enheten trenger et navn", http.StatusBadRequest)
		return
	}
	err := s.store.UpdateUnit(r.PathValue("id"), user.TenantID, u)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ukjent enhet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "kunne ikke oppdatere enheten", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteUnit(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(store.User)
	err := s.store.DeleteUnit(r.PathValue("id"), user.TenantID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ukjent enhet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "kunne ikke slette enheten", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleOrgMe gir klienten brukerens org-posisjon — grunnlaget for
// scope-valgene i opplasting og minnekort («Min enhet» vises kun når den
// finnes).
func (s *Server) handleOrgMe(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(store.User)
	sc, _ := s.store.ScopeFor(user.TenantID, user.ID)
	type myUnit struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var mine []myUnit
	if len(sc.UnitIDs) > 0 {
		if units, err := s.store.ListUnits(user.TenantID); err == nil {
			for _, u := range units {
				if sc.Has(u.ID) {
					mine = append(mine, myUnit{ID: u.ID, Name: u.Name})
				}
			}
		}
	}
	writeJSON(w, map[string]any{"units": mine, "role": sc.Role})
}
