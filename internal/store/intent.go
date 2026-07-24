package store

import "time"

// IntentDecision er én logget motoravgjørelse. Dette er både observasjons-
// grunnlaget for å sammenligne motoren mot dagens oppførsel (shadow-modus) og
// treningssettet for en fremtidig finetunet klassifiserer. `Corrected` fylles
// når en avgjørelse i ettertid merkes med riktig nøkkel (fasit).
type IntentDecision struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"-"`
	UserID     string    `json:"-"`
	Message    string    `json:"message"`
	Key        string    `json:"key"`    // valgt flyt ("" = fri chat)
	Method     string    `json:"method"` // direct | judge | none | multi
	Candidates string    `json:"candidates"` // JSON [{key,score}...]
	ElapsedMS  int       `json:"elapsed_ms"`
	Corrected  string    `json:"corrected"` // fasit satt i ettertid (tom = ukjent)
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) migrateIntentDecisions() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS intent_decisions (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			message    TEXT NOT NULL,
			key        TEXT NOT NULL DEFAULT '',
			method     TEXT NOT NULL,
			candidates TEXT NOT NULL DEFAULT '[]',
			elapsed_ms INTEGER NOT NULL DEFAULT 0,
			corrected  TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_intent_decisions_created ON intent_decisions (created_at);
	`)
	return err
}

// InsertIntentDecision logger én avgjørelse. Feil her skal aldri påvirke
// chatten — kalleren logger og går videre.
func (s *Store) InsertIntentDecision(d IntentDecision) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO intent_decisions (id, tenant_id, user_id, message, key, method, candidates, elapsed_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, d.TenantID, d.UserID, d.Message, d.Key, d.Method, d.Candidates, d.ElapsedMS,
	)
	return err
}
