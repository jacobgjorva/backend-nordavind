package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

type ctxKey string

const userKey ctxKey = "user"

// handleRequestCode lager en engangskode for en registrert bruker.
// MVP: koden logges på serveren i stedet for å sendes på e-post.
func (s *Server) handleRequestCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		http.Error(w, "mangler e-post", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Samme respons uansett om brukeren finnes — ingen e-post-enumerering.
	if _, err := s.store.UserByEmail(email); err == nil {
		code, err := s.store.CreateLoginCode(email)
		if err != nil {
			http.Error(w, "intern feil", http.StatusInternalServerError)
			return
		}
		// TODO: send e-post. Inntil videre: hent koden fra server-loggen.
		s.log.Info("innloggingskode", "email", email, "code", code)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleVerifyCode bytter e-post + kode mot en sesjonstoken.
func (s *Server) handleVerifyCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Code == "" {
		http.Error(w, "mangler e-post eller kode", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))

	token, user, err := s.store.RedeemCode(email, strings.TrimSpace(req.Code))
	if errors.Is(err, store.ErrInvalidCode) || errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ugyldig kode", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"token": token, "user": user})
}

// handleMe returnerer innlogget bruker og tenant.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(userKey).(store.User)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	tenant, err := s.store.TenantByID(user.TenantID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"user": user, "tenant": tenant})
}

// requireAuth beskytter et endepunkt med sesjonstoken. Kan skrus av i dev
// med AUTH_REQUIRED=false (default av inntil frontend har innlogging).
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "" {
			if user, err := s.store.UserBySession(token); err == nil {
				// Admin-simulering: en admin kan tre inn i en annen brukers
				// tilstand (X-Impersonate-User) — kun innen egen tenant.
				if imp := strings.TrimSpace(r.Header.Get("X-Impersonate-User")); imp != "" && imp != user.ID && user.Role == "admin" {
					if target, err := s.store.UserByID(imp); err == nil && target.TenantID == user.TenantID {
						s.log.Info("admin simulerer bruker", "admin", user.Email, "som", target.Email)
						user = target
					}
				}
				next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
				return
			}
		}
		if !s.cfg.AuthRequired {
			next(w, r)
			return
		}
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
	}
}
