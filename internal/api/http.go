package api

import (
	"encoding/json"
	"net/http"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// user henter innlogget bruker fra kontekst og skriver 401 selv hvis den
// mangler. Returnerer ok=false når handleren skal returnere umiddelbart.
func (s *Server) user(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userKey).(store.User)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "ikke innlogget")
	}
	return u, ok
}

// writeJSON skriver v som JSON med riktig content-type.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// writeErr skriver en ren tekst-feil med statuskoden.
func writeErr(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}
