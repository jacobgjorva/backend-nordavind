package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Widget er én navngitt visualisering brukeren kan kalle overalt med
// /<slug>. Spec er JSON for én komponent (kpi/text/table/bar/line) og
// serialiseres som et objekt (RawMessage), ikke en streng.
type Widget struct {
	ID        string          `json:"id"`
	Slug      string          `json:"slug"`
	Title     string          `json:"title"`
	Spec      json.RawMessage `json:"spec,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (s *Store) migrateWidgets() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS widgets (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			slug       TEXT NOT NULL,
			title      TEXT NOT NULL DEFAULT '',
			spec       TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_widgets_slug ON widgets (user_id, slug);
	`)
	return err
}

// CreateWidget oppretter en tom widget med gitt slug. Slug er unik per bruker.
func (s *Store) CreateWidget(tenantID, userID, slug, title string) (Widget, error) {
	w := Widget{ID: newID(), Slug: slug, Title: title, Spec: json.RawMessage("{}"), UpdatedAt: time.Now()}
	_, err := s.db.Exec(
		`INSERT INTO widgets (id, tenant_id, user_id, slug, title, spec) VALUES (?, ?, ?, ?, ?, '{}')`,
		w.ID, tenantID, userID, slug, title,
	)
	return w, err
}

// ListWidgets returnerer brukerens widgets, nyest først (uten spec).
func (s *Store) ListWidgets(userID string) ([]Widget, error) {
	rows, err := s.db.Query(
		`SELECT id, slug, title, updated_at FROM widgets WHERE user_id = ? ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Widget
	for rows.Next() {
		var w Widget
		if err := rows.Scan(&w.ID, &w.Slug, &w.Title, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Widget henter én widget brukeren eier, med spec.
func (s *Store) Widget(slug, userID string) (Widget, error) {
	var w Widget
	var spec string
	err := s.db.QueryRow(
		`SELECT id, slug, title, spec, updated_at FROM widgets WHERE slug = ? AND user_id = ?`,
		slug, userID,
	).Scan(&w.ID, &w.Slug, &w.Title, &spec, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if spec == "" {
		spec = "{}"
	}
	w.Spec = json.RawMessage(spec)
	return w, err
}

// SetWidget lagrer spec (og evt. tittel) for en widget brukeren eier.
func (s *Store) SetWidget(slug, userID, title, spec string) error {
	res, err := s.db.Exec(
		`UPDATE widgets SET title = ?, spec = ?, updated_at = ? WHERE slug = ? AND user_id = ?`,
		title, spec, time.Now(), slug, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWidget fjerner en widget brukeren eier.
func (s *Store) DeleteWidget(slug, userID string) error {
	res, err := s.db.Exec(`DELETE FROM widgets WHERE slug = ? AND user_id = ?`, slug, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
