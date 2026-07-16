package store

import "time"

// UsageEvent er én ferdig chat-forespørsel med tokenforbruk og kost.
type UsageEvent struct {
	TenantID         string
	UserID           string
	Model            string
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64
	Searches         int
	DurationMS       int64
}

// DailyUsage er aggregert forbruk for én dag og modell.
type DailyUsage struct {
	Day              string  `json:"day"`
	Model            string  `json:"model"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Searches         int     `json:"searches"`
}

func (s *Store) migrateUsage() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS usage_events (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id         TEXT NOT NULL DEFAULT '',
			user_id           TEXT NOT NULL DEFAULT '',
			model             TEXT NOT NULL,
			prompt_tokens     INTEGER NOT NULL,
			completion_tokens INTEGER NOT NULL,
			cost_usd          REAL NOT NULL,
			searches          INTEGER NOT NULL DEFAULT 0,
			duration_ms       INTEGER NOT NULL DEFAULT 0,
			created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_usage_tenant_day ON usage_events (tenant_id, created_at);
	`)
	return err
}

func (s *Store) InsertUsage(e UsageEvent) error {
	_, err := s.db.Exec(
		`INSERT INTO usage_events
		 (tenant_id, user_id, model, prompt_tokens, completion_tokens, cost_usd, searches, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TenantID, e.UserID, e.Model, e.PromptTokens, e.CompletionTokens,
		e.CostUSD, e.Searches, e.DurationMS,
	)
	return err
}

// DailyUsage aggregerer per dag og modell. userID == "" gir hele tenanten.
func (s *Store) DailyUsage(tenantID, userID string, days int) ([]DailyUsage, error) {
	since := time.Now().AddDate(0, 0, -days)
	query := `
		SELECT date(created_at), model, COUNT(*),
		       SUM(prompt_tokens), SUM(completion_tokens), SUM(cost_usd), SUM(searches)
		FROM usage_events
		WHERE tenant_id = ? AND created_at >= ?`
	args := []any{tenantID, since}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` GROUP BY date(created_at), model ORDER BY date(created_at)`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Day, &d.Model, &d.Requests,
			&d.PromptTokens, &d.CompletionTokens, &d.CostUSD, &d.Searches); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
