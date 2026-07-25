package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// handleTestConnection prøver å koble til uten å lagre noe.
func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var creds connector.Creds
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	db, err := connector.Open(r.Context(), creds)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	db.Close()
	w.WriteHeader(http.StatusNoContent)
}

// handleCreateConnection tester tilkoblingen og lagrer kredensialene kryptert.
func (s *Server) handleCreateConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
		connector.Creds
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Host == "" {
		http.Error(w, "navn, type og host må være satt", http.StatusBadRequest)
		return
	}

	db, err := connector.Open(r.Context(), req.Creds)
	if err != nil {
		s.log.Warn("tilkobling feilet", "err", err)
		http.Error(w, "kunne ikke koble til databasen: "+err.Error(), http.StatusBadGateway)
		return
	}
	db.Close()

	plain, _ := json.Marshal(req.Creds)
	enc, err := connector.Encrypt(s.credsKey, plain)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	conn, err := s.store.CreateConnection(user.TenantID, req.Name, req.Driver, enc)
	if err != nil {
		http.Error(w, "kunne ikke lagre tilkoblingen", http.StatusInternalServerError)
		return
	}
	s.log.Info("tilkobling opprettet", "tenant", user.TenantID, "driver", req.Driver)
	writeJSON(w, conn)
}

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	conns, err := s.store.ListConnections(user.TenantID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if conns == nil {
		conns = []store.Connection{}
	}
	writeJSON(w, map[string]any{"connections": conns})
}

func (s *Server) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteConnection(user.TenantID, r.PathValue("id")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// openConnection henter og dekrypterer kredensialer, og åpner tilkoblingen.
func (s *Server) openConnection(r *http.Request, id string) (store.Connection, connector.Creds, *sql.DB, error) {
	user, _ := r.Context().Value(userKey).(store.User)
	conn, enc, err := s.store.ConnectionCreds(user.TenantID, id)
	if err != nil {
		return conn, connector.Creds{}, nil, err
	}
	plain, err := connector.Decrypt(s.credsKey, enc)
	if err != nil {
		return conn, connector.Creds{}, nil, err
	}
	var creds connector.Creds
	if err := json.Unmarshal(plain, &creds); err != nil {
		return conn, creds, nil, err
	}
	db, err := connector.Open(r.Context(), creds)
	if err != nil {
		return conn, creds, nil, err
	}
	return conn, creds, db, nil
}

// handleConnectionSchema introspekterer live-skjema og fletter inn lagret
// kuratering (beskrivelser, tilgang, lenker).
func (s *Server) handleConnectionSchema(w http.ResponseWriter, r *http.Request) {
	conn, creds, db, err := s.openConnection(r, r.PathValue("id"))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "kunne ikke koble til: "+err.Error(), status)
		return
	}
	defer db.Close()

	tables, fks, err := connector.Introspect(r.Context(), db, creds)
	if err != nil {
		http.Error(w, "introspeksjon feilet: "+err.Error(), http.StatusBadGateway)
		return
	}
	saved, links, err := s.store.ConnectionConfig(conn.ID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	views, err := s.store.ConnectionViews(conn.ID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"connection":      conn,
		"tables":          tables,
		"suggested_links": fks,
		"config":          map[string]any{"tables": saved, "links": links, "views": views},
	})
}

// handleSaveConnectionConfig lagrer admin-kurateringen for en tilkobling.
func (s *Server) handleSaveConnectionConfig(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, _, err := s.store.ConnectionCreds(user.TenantID, id); err != nil {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	var req struct {
		Tables []store.TableConfig `json:"tables"`
		Links  []store.LinkConfig  `json:"links"`
		Views  []store.ViewConfig  `json:"views"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}

	// Introspekter live slik at hele kolonnelisten (med typer) lagres —
	// AI-en skal se hele skjemaet, ikke bare feltene med beskrivelse.
	_, creds, db, err := s.openConnection(r, id)
	if err != nil {
		http.Error(w, "kunne ikke koble til: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer db.Close()
	live, _, err := connector.Introspect(r.Context(), db, creds)
	if err != nil {
		http.Error(w, "introspeksjon feilet: "+err.Error(), http.StatusBadGateway)
		return
	}
	colTypes := map[string]map[string]string{}
	for _, t := range live {
		m := map[string]string{}
		for _, c := range t.Columns {
			m[c.Name] = c.Type
		}
		colTypes[t.Name] = m
	}

	if err := s.store.SaveConnectionConfig(id, req.Tables, req.Links, req.Views, colTypes); err != nil {
		http.Error(w, "kunne ikke lagre", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
