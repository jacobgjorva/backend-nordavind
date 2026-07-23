package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// handleUsageDaily returnerer aggregert forbruk per dag og modell.
// Admin ser hele tenanten; andre ser bare eget forbruk.
func (s *Server) handleUsageDaily(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(userKey).(store.User)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}

	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}

	userID := user.ID
	if user.Role == "admin" && r.URL.Query().Get("scope") == "tenant" {
		userID = ""
	}

	rows, err := s.store.DailyUsage(user.TenantID, userID, days)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []store.DailyUsage{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"days":    days,
		"usage":   rows,
		"usd_nok": s.rates.get(r.Context(), s.client),
	})
}
