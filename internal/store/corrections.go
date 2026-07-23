package store

// Correction er en brukerkorrigering på et AI-svar, logget for senere
// opptrening på tenantens interne kontekst.
type Correction struct {
	AnswerContent string `json:"answer"`
	Correction    string `json:"correction"`
	ChatID        string `json:"chat_id,omitempty"`
}

func (s *Store) migrateCorrections() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS corrections (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			chat_id    TEXT NOT NULL DEFAULT '',
			answer     TEXT NOT NULL,
			correction TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_corrections_tenant ON corrections (tenant_id, created_at);
	`)
	return err
}

// LogCorrection lagrer en korrigering knyttet til tenant og bruker.
func (s *Store) LogCorrection(tenantID, userID string, c Correction) error {
	_, err := s.db.Exec(
		`INSERT INTO corrections (tenant_id, user_id, chat_id, answer, correction) VALUES (?, ?, ?, ?, ?)`,
		tenantID, userID, c.ChatID, c.AnswerContent, c.Correction,
	)
	return err
}
