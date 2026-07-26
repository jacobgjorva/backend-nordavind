package store

import (
	"database/sql"
	"errors"
	"time"
)

// ExportLink er en live Excel-kobling: et hemmelig token (lagret som hash) som
// gir tilgang til å kjøre ÉN lagret, skrivebeskyttet spørring — ingenting annet.
// Lekker lenken, lekker kun akkurat dette datasettet, og den kan trekkes tilbake.
type ExportLink struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	ConnectionID string     `json:"connection_id"`
	SQL          string     `json:"-"` // aldri ut til klient
	TenantID     string     `json:"-"`
	UserID       string     `json:"-"`
	Revoked      bool       `json:"revoked"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
}

func (s *Store) migrateExportLinks() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS export_links (
			id            TEXT PRIMARY KEY,
			tenant_id     TEXT NOT NULL,
			user_id       TEXT NOT NULL,
			title         TEXT NOT NULL DEFAULT '',
			connection_id TEXT NOT NULL DEFAULT '',
			sql_text      TEXT NOT NULL,
			token_hash    TEXT NOT NULL UNIQUE,
			revoked       INTEGER NOT NULL DEFAULT 0,
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_used_at  TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_export_links_user ON export_links (user_id, created_at);
	`)
	return err
}

// CreateExportLink lagrer en live-lenke. Selve tokenet lagres ALDRI — kun hashen.
func (s *Store) CreateExportLink(tenantID, userID, title, connectionID, sqlText, tokenHash string) (ExportLink, error) {
	id, err := newID()
	if err != nil {
		return ExportLink{}, err
	}
	l := ExportLink{
		ID: id, Title: title, ConnectionID: connectionID, SQL: sqlText,
		TenantID: tenantID, UserID: userID, CreatedAt: time.Now(),
	}
	_, err = s.db.Exec(
		`INSERT INTO export_links (id, tenant_id, user_id, title, connection_id, sql_text, token_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.ID, tenantID, userID, title, connectionID, sqlText, tokenHash,
	)
	return l, err
}

// ExportLinkByTokenHash slår opp en aktiv (ikke-tilbakekalt) live-lenke.
func (s *Store) ExportLinkByTokenHash(tokenHash string) (ExportLink, error) {
	var l ExportLink
	var revoked int
	err := s.db.QueryRow(
		`SELECT id, tenant_id, user_id, title, connection_id, sql_text, revoked, created_at
		 FROM export_links WHERE token_hash = ?`,
		tokenHash,
	).Scan(&l.ID, &l.TenantID, &l.UserID, &l.Title, &l.ConnectionID, &l.SQL, &revoked, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	l.Revoked = revoked != 0
	if l.Revoked {
		return l, ErrNotFound
	}
	return l, nil
}

// TouchExportLink noterer siste bruk (til innsyn/opprydding).
func (s *Store) TouchExportLink(id string) {
	s.db.Exec(`UPDATE export_links SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
}

// ListExportLinks returnerer brukerens live-lenker (uten SQL/token).
func (s *Store) ListExportLinks(userID string) ([]ExportLink, error) {
	rows, err := s.db.Query(
		`SELECT id, title, connection_id, revoked, created_at, last_used_at
		 FROM export_links WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportLink
	for rows.Next() {
		var l ExportLink
		var revoked int
		if err := rows.Scan(&l.ID, &l.Title, &l.ConnectionID, &revoked, &l.CreatedAt, &l.LastUsedAt); err != nil {
			return nil, err
		}
		l.Revoked = revoked != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeExportLink trekker tilbake en live-lenke (fila slutter å kunne oppdatere).
func (s *Store) RevokeExportLink(id, userID string) error {
	res, err := s.db.Exec(
		`UPDATE export_links SET revoked = 1 WHERE id = ? AND user_id = ?`, id, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
