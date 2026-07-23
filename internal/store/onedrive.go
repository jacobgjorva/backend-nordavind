package store

import "time"

// OneDriveExport er en live arbeidsbok i brukerens OneDrive: vi pusher ferske
// tall fra den lagrede spørringen inn i fila på fast takt.
type OneDriveExport struct {
	ID           string
	TenantID     string
	UserID       string
	Title        string
	ConnectionID string
	SQL          string
	DriveItemID  string
	WebURL       string
	CreatedAt    time.Time
}

func (s *Store) migrateOneDrive() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS onedrive_exports (
			id            TEXT PRIMARY KEY,
			tenant_id     TEXT NOT NULL,
			user_id       TEXT NOT NULL,
			title         TEXT NOT NULL DEFAULT '',
			connection_id TEXT NOT NULL DEFAULT '',
			sql_text      TEXT NOT NULL,
			drive_item_id TEXT NOT NULL,
			web_url       TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_push_at  TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_onedrive_user ON onedrive_exports (user_id, created_at);
	`)
	return err
}

// CreateOneDriveExport lagrer en live OneDrive-eksport.
func (s *Store) CreateOneDriveExport(e OneDriveExport) (OneDriveExport, error) {
	id, err := newID()
	if err != nil {
		return e, err
	}
	e.ID = id
	e.CreatedAt = time.Now()
	_, err = s.db.Exec(
		`INSERT INTO onedrive_exports (id, tenant_id, user_id, title, connection_id, sql_text, drive_item_id, web_url)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TenantID, e.UserID, e.Title, e.ConnectionID, e.SQL, e.DriveItemID, e.WebURL,
	)
	return e, err
}

// ListAllOneDriveExports returnerer alle live-eksporter (til push-syklusen).
func (s *Store) ListAllOneDriveExports() ([]OneDriveExport, error) {
	rows, err := s.db.Query(
		`SELECT id, tenant_id, user_id, title, connection_id, sql_text, drive_item_id, web_url, created_at
		 FROM onedrive_exports`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OneDriveExport
	for rows.Next() {
		var e OneDriveExport
		if err := rows.Scan(&e.ID, &e.TenantID, &e.UserID, &e.Title, &e.ConnectionID,
			&e.SQL, &e.DriveItemID, &e.WebURL, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TouchOneDriveExport noterer siste push.
func (s *Store) TouchOneDriveExport(id string) {
	s.db.Exec(`UPDATE onedrive_exports SET last_push_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
}

// DeleteOneDriveExport fjerner en live-eksport (fila i OneDrive består).
func (s *Store) DeleteOneDriveExport(id, userID string) error {
	_, err := s.db.Exec(`DELETE FROM onedrive_exports WHERE id = ? AND user_id = ?`, id, userID)
	return err
}
