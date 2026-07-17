package store

import (
	"database/sql"
	"errors"
	"time"
)

// Chat er en samtale eid av én bruker i en tenant.
type Chat struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChatMessage er én melding i en samtale. Sources er JSON fra websøk.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Sources string `json:"sources,omitempty"`
}

func (s *Store) migrateChats() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS chats (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			title      TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_chats_user ON chats (user_id, updated_at);
		CREATE TABLE IF NOT EXISTS chat_messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id    TEXT NOT NULL REFERENCES chats(id),
			role       TEXT NOT NULL,
			content    TEXT NOT NULL,
			sources    TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_messages_chat ON chat_messages (chat_id, id);
	`)
	return err
}

func (s *Store) CreateChat(tenantID, userID, title string) (Chat, error) {
	c := Chat{ID: newID(), Title: title, UpdatedAt: time.Now()}
	_, err := s.db.Exec(
		`INSERT INTO chats (id, tenant_id, user_id, title) VALUES (?, ?, ?, ?)`,
		c.ID, tenantID, userID, c.Title,
	)
	return c, err
}

// ListChats returnerer brukerens samtaler, nyest oppdatert først.
func (s *Store) ListChats(userID string) ([]Chat, error) {
	rows, err := s.db.Query(
		`SELECT id, title, updated_at FROM chats WHERE user_id = ? ORDER BY updated_at DESC LIMIT 100`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Title, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// chatOwned sjekker at samtalen tilhører brukeren.
func (s *Store) chatOwned(chatID, userID string) error {
	var one int
	err := s.db.QueryRow(
		`SELECT 1 FROM chats WHERE id = ? AND user_id = ?`, chatID, userID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ChatMessages returnerer meldingene i en samtale brukeren eier.
func (s *Store) ChatMessages(chatID, userID string) ([]ChatMessage, error) {
	if err := s.chatOwned(chatID, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT role, content, sources FROM chat_messages WHERE chat_id = ? ORDER BY id`,
		chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.Role, &m.Content, &m.Sources); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendMessage legger en melding til en samtale brukeren eier.
func (s *Store) AppendMessage(chatID, userID string, m ChatMessage) error {
	if err := s.chatOwned(chatID, userID); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`INSERT INTO chat_messages (chat_id, role, content, sources) VALUES (?, ?, ?, ?)`,
		chatID, m.Role, m.Content, m.Sources,
	); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE chats SET updated_at = ? WHERE id = ?`, time.Now(), chatID)
	return err
}

// DeleteChat fjerner en samtale med meldinger.
func (s *Store) DeleteChat(chatID, userID string) error {
	if err := s.chatOwned(chatID, userID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM chat_messages WHERE chat_id = ?`, chatID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM chats WHERE id = ?`, chatID)
	return err
}
