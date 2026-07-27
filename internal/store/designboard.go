package store

import "time"

// Designarbeidsområdet: én design-chat holder MANGE dokumenter, lagt utover
// et uendelig board. Denne tabellen er bare plasseringen — selve dokumentet
// ligger i widgets, som før. Da kan brukeren eksperimentere med varianter
// side om side uten at noe av motoren endres.

// BoardItem er ett dokument plassert i et arbeidsområde.
type BoardItem struct {
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	X         float64   `json:"x"`
	Y         float64   `json:"y"`
	Spec      string    `json:"spec"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) migrateDesignBoard() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS design_items (
			chat_id    TEXT NOT NULL,
			slug       TEXT NOT NULL,
			x          REAL NOT NULL DEFAULT 0,
			y          REAL NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (chat_id, slug)
		);
		CREATE INDEX IF NOT EXISTS idx_design_items_chat ON design_items (chat_id);
	`)
	return err
}

// PlaceOnBoard legger et dokument i arbeidsområdet (eller flytter det).
func (s *Store) PlaceOnBoard(chatID, slug string, x, y float64) error {
	_, err := s.db.Exec(
		`INSERT INTO design_items (chat_id, slug, x, y) VALUES (?, ?, ?, ?)
		 ON CONFLICT (chat_id, slug) DO UPDATE SET x = EXCLUDED.x, y = EXCLUDED.y`,
		chatID, slug, x, y,
	)
	return err
}

// BoardItems gir dokumentene i arbeidsområdet med spec, i den rekkefølgen de
// ble laget. Eierskap håndheves via widgets.user_id.
func (s *Store) BoardItems(chatID, userID string) ([]BoardItem, error) {
	rows, err := s.db.Query(
		`SELECT i.slug, w.title, i.x, i.y, w.spec, w.updated_at
		 FROM design_items i JOIN widgets w ON w.slug = i.slug AND w.user_id = ?
		 WHERE i.chat_id = ? ORDER BY i.created_at`,
		userID, chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BoardItem
	for rows.Next() {
		var it BoardItem
		if err := rows.Scan(&it.Slug, &it.Title, &it.X, &it.Y, &it.Spec, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// RemoveFromBoard tar dokumentet ut av arbeidsområdet (selve dokumentet blir
// stående — brukeren kan ha delt det).
func (s *Store) RemoveFromBoard(chatID, slug string) error {
	_, err := s.db.Exec(`DELETE FROM design_items WHERE chat_id = ? AND slug = ?`, chatID, slug)
	return err
}
