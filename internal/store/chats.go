package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Chat er en samtale eid av én bruker i en tenant. AgentID er satt hvis
// samtalen tilhører en agent (grupperes under «Agenter» i sidepanelet).
type Chat struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	UpdatedAt    time.Time `json:"updated_at"`
	AgentID      string    `json:"agent_id,omitempty"`
	AgentEnabled bool      `json:"agent_enabled,omitempty"`
	Kind         string    `json:"kind,omitempty"`      // "chat" | "dashboard"
	FolderID     string    `json:"folder_id,omitempty"` // mappe chatten ligger i (tom = ingen)
}

// Folder er en brukeropprettet mappe for å organisere chatter i sidebaren.
type Folder struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ChatMessage er én melding i en samtale. Sources er JSON fra websøk.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Sources string `json:"sources,omitempty"`
}

func (s *Store) migrateChats() error {
	if _, err := s.db.Exec(`
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
	`); err != nil {
		return err
	}
	for _, col := range []string{
		`ALTER TABLE chats ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE chats ADD COLUMN kind TEXT NOT NULL DEFAULT 'chat'`,
		`ALTER TABLE chats ADD COLUMN dashboard_spec TEXT NOT NULL DEFAULT '{"components":[]}'`,
		`ALTER TABLE chats ADD COLUMN folder_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS folders (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			name       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_folders_user ON folders (user_id, created_at);
	`); err != nil {
		return err
	}
	return nil
}

// CreateFolder oppretter en mappe for brukeren.
func (s *Store) CreateFolder(tenantID, userID, name string) (Folder, error) {
	id, err := newID()
	if err != nil {
		return Folder{}, err
	}
	f := Folder{ID: id, Name: name, CreatedAt: time.Now()}
	_, err = s.db.Exec(
		`INSERT INTO folders (id, tenant_id, user_id, name) VALUES (?, ?, ?, ?)`,
		f.ID, tenantID, userID, f.Name,
	)
	return f, err
}

// ListFolders returnerer brukerens mapper, eldst først (stabil rekkefølge).
func (s *Store) ListFolders(userID string) ([]Folder, error) {
	rows, err := s.db.Query(
		`SELECT id, name, created_at FROM folders WHERE user_id = ? ORDER BY created_at`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RenameFolder endrer navnet på en mappe brukeren eier.
func (s *Store) RenameFolder(id, userID, name string) error {
	res, err := s.db.Exec(`UPDATE folders SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFolder sletter en mappe og løsner chattene (de havner i historikken igjen).
func (s *Store) DeleteFolder(id, userID string) error {
	res, err := s.db.Exec(`DELETE FROM folders WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.Exec(`UPDATE chats SET folder_id = '' WHERE folder_id = ? AND user_id = ?`, id, userID)
	return err
}

// SetChatFolder flytter en chat inn i en mappe (tom folderID = ut av mappe).
func (s *Store) SetChatFolder(chatID, userID, folderID string) error {
	res, err := s.db.Exec(
		`UPDATE chats SET folder_id = ? WHERE id = ? AND user_id = ?`, folderID, chatID, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateChat(tenantID, userID, title string) (Chat, error) {
	id, err := newID()
	if err != nil {
		return Chat{}, err
	}
	c := Chat{ID: id, Title: title, UpdatedAt: time.Now()}
	_, err = s.db.Exec(
		`INSERT INTO chats (id, tenant_id, user_id, title) VALUES (?, ?, ?, ?)`,
		c.ID, tenantID, userID, c.Title,
	)
	return c, err
}

// ListChats returnerer brukerens samtaler, nyest oppdatert først.
func (s *Store) ListChats(userID string) ([]Chat, error) {
	rows, err := s.db.Query(
		`SELECT c.id, c.title, c.updated_at, c.agent_id, COALESCE(a.enabled, 0), c.kind, c.folder_id
		 FROM chats c LEFT JOIN agents a ON a.id = c.agent_id
		 WHERE c.user_id = ? ORDER BY c.updated_at DESC LIMIT 100`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Chat
	for rows.Next() {
		var c Chat
		var enabled Flag
		if err := rows.Scan(&c.ID, &c.Title, &c.UpdatedAt, &c.AgentID, &enabled, &c.Kind, &c.FolderID); err != nil {
			return nil, err
		}
		c.AgentEnabled = bool(enabled)
		out = append(out, c)
	}
	return out, rows.Err()
}

// AppendAgentMessage legger en melding til en agent-chat uten eierskapssjekk
// (scheduleren kjører uten brukersesjon).
func (s *Store) AppendAgentMessage(chatID string, m ChatMessage) error {
	if _, err := s.db.Exec(
		`INSERT INTO chat_messages (chat_id, role, content, sources) VALUES (?, ?, ?, ?)`,
		chatID, m.Role, m.Content, m.Sources,
	); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE chats SET updated_at = ? WHERE id = ?`, time.Now(), chatID)
	return err
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
	// Er dette en agent-chat, fjern også agenten (og løggen) så den ikke blir
	// liggende igjen og kjøre. En kjørende oppdrags-løkke ser at agenten er borte
	// og stopper av seg selv.
	if _, err := s.db.Exec(
		`DELETE FROM agent_runs WHERE agent_id IN (SELECT id FROM agents WHERE chat_id = ?)`, chatID,
	); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM agents WHERE chat_id = ?`, chatID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM chats WHERE id = ?`, chatID)
	return err
}

// UpdateChatTitle setter ny tittel på en samtale brukeren eier.
func (s *Store) UpdateChatTitle(chatID, userID, title string) error {
	if err := s.chatOwned(chatID, userID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE chats SET title = ? WHERE id = ?`, title, chatID)
	return err
}
