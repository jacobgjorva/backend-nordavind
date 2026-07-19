package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// KnowledgeNode er én atomær bransje-fakta i tenantens kunnskapsgraf.
// Status styrer governance: kun "accepted" noder brukes av AI-en.
type KnowledgeNode struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`    // entitet | term | prosess | regel
	Title     string    `json:"title"`   // kort navn
	Summary   string    `json:"summary"` // én linje
	Status    string    `json:"status"`  // pending | accepted | rejected
	ChatID    string    `json:"chat_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	UserEmail string    `json:"user_email,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Embedding []float32 `json:"-"`
}

// KnowledgeEdge er en typet relasjon mellom to noder.
type KnowledgeEdge struct {
	FromID   string `json:"from_id"`
	ToID     string `json:"to_id"`
	Relation string `json:"relation"`
}

func (s *Store) migrateKnowledge() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS knowledge_nodes (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			type       TEXT NOT NULL DEFAULT 'term',
			title      TEXT NOT NULL,
			summary    TEXT NOT NULL,
			status     TEXT NOT NULL DEFAULT 'pending',
			chat_id    TEXT NOT NULL DEFAULT '',
			user_id    TEXT NOT NULL DEFAULT '',
			embedding  TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_knodes_tenant ON knowledge_nodes (tenant_id, status);
		CREATE TABLE IF NOT EXISTS knowledge_edges (
			tenant_id TEXT NOT NULL,
			from_id   TEXT NOT NULL,
			to_id     TEXT NOT NULL,
			relation  TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (from_id, to_id, relation)
		);
		CREATE INDEX IF NOT EXISTS idx_kedges_from ON knowledge_edges (from_id);
		CREATE INDEX IF NOT EXISTS idx_kedges_to ON knowledge_edges (to_id);
	`)
	return err
}

// CreateNode lagrer en node (default pending). Embedding lagres som JSON.
func (s *Store) CreateNode(tenantID string, n KnowledgeNode) (KnowledgeNode, error) {
	n.ID = newID()
	n.CreatedAt = time.Now()
	if n.Status == "" {
		n.Status = "pending"
	}
	if n.Type == "" {
		n.Type = "term"
	}
	emb, _ := json.Marshal(n.Embedding)
	_, err := s.db.Exec(
		`INSERT INTO knowledge_nodes (id, tenant_id, type, title, summary, status, chat_id, user_id, embedding)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, tenantID, n.Type, n.Title, n.Summary, n.Status, n.ChatID, n.UserID, string(emb),
	)
	return n, err
}

// AddEdge lagrer en relasjon mellom to noder (idempotent).
func (s *Store) AddEdge(tenantID string, e KnowledgeEdge) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO knowledge_edges (tenant_id, from_id, to_id, relation)
		 VALUES (?, ?, ?, ?)`,
		tenantID, e.FromID, e.ToID, e.Relation,
	)
	return err
}

// AcceptedNodes henter tenantens aksepterte noder med embeddings (for henting).
func (s *Store) AcceptedNodes(tenantID string) ([]KnowledgeNode, error) {
	rows, err := s.db.Query(
		`SELECT id, type, title, summary, embedding FROM knowledge_nodes
		 WHERE tenant_id = ? AND status = 'accepted'`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KnowledgeNode
	for rows.Next() {
		var n KnowledgeNode
		var emb string
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Summary, &emb); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(emb), &n.Embedding)
		out = append(out, n)
	}
	return out, rows.Err()
}

// NeighborSummaries henter én-linjes summary for nabonoder (1-hopp) til de
// oppgitte node-IDene, kun aksepterte.
func (s *Store) NeighborSummaries(tenantID string, ids []string) ([]KnowledgeNode, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := `SELECT DISTINCT n.id, n.type, n.title, n.summary
	      FROM knowledge_nodes n
	      JOIN knowledge_edges e ON (e.to_id = n.id OR e.from_id = n.id)
	      WHERE n.tenant_id = ? AND n.status = 'accepted' AND (`
	args := []any{tenantID}
	for i, id := range ids {
		if i > 0 {
			q += " OR "
		}
		q += "e.from_id = ? OR e.to_id = ?"
		args = append(args, id, id)
	}
	q += ")"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeNode
	for rows.Next() {
		var n KnowledgeNode
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Summary); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NodeTitles henter alle node-titler for en tenant (til dedup i uttrekk).
func (s *Store) NodeTitles(tenantID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT title FROM knowledge_nodes WHERE tenant_id = ? AND status != 'rejected'`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PendingNodes henter noder som venter på admin-godkjenning, eldste først.
func (s *Store) PendingNodes(tenantID string) ([]KnowledgeNode, error) {
	rows, err := s.db.Query(
		`SELECT n.id, n.type, n.title, n.summary, n.created_at, COALESCE(u.email, '')
		 FROM knowledge_nodes n LEFT JOIN users u ON u.id = n.user_id
		 WHERE n.tenant_id = ? AND n.status = 'pending' ORDER BY n.created_at`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeNode
	for rows.Next() {
		var n KnowledgeNode
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Summary, &n.CreatedAt, &n.UserEmail); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AcceptNode godkjenner en node med (evt. redigert) tittel/summary og ny embedding.
func (s *Store) AcceptNode(id, tenantID, title, summary string, embedding []float32) error {
	emb, _ := json.Marshal(embedding)
	res, err := s.db.Exec(
		`UPDATE knowledge_nodes SET title = ?, summary = ?, embedding = ?, status = 'accepted'
		 WHERE id = ? AND tenant_id = ? AND status = 'pending'`,
		title, summary, string(emb), id, tenantID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateNode redigerer en akseptert node manuelt og reberegner embedding.
func (s *Store) UpdateNode(id, tenantID, title, summary string, embedding []float32) error {
	emb, _ := json.Marshal(embedding)
	res, err := s.db.Exec(
		`UPDATE knowledge_nodes SET title = ?, summary = ?, embedding = ?
		 WHERE id = ? AND tenant_id = ?`,
		title, summary, string(emb), id, tenantID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteNode fjerner en node og alle kantene den inngår i.
func (s *Store) DeleteNode(id, tenantID string) error {
	res, err := s.db.Exec(
		`DELETE FROM knowledge_nodes WHERE id = ? AND tenant_id = ?`, id, tenantID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.db.Exec(`DELETE FROM knowledge_edges WHERE from_id = ? OR to_id = ?`, id, id)
	return nil
}

// RejectNode markerer en node som avvist (huskes så den ikke foreslås igjen).
func (s *Store) RejectNode(id, tenantID string) error {
	res, err := s.db.Exec(
		`UPDATE knowledge_nodes SET status = 'rejected'
		 WHERE id = ? AND tenant_id = ? AND status = 'pending'`,
		id, tenantID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GraphData henter aksepterte noder og kantene mellom dem (til visualisering).
func (s *Store) GraphData(tenantID string) ([]KnowledgeNode, []KnowledgeEdge, error) {
	nodeRows, err := s.db.Query(
		`SELECT id, type, title, summary FROM knowledge_nodes
		 WHERE tenant_id = ? AND status = 'accepted'`,
		tenantID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer nodeRows.Close()
	var nodes []KnowledgeNode
	live := map[string]bool{}
	for nodeRows.Next() {
		var n KnowledgeNode
		if err := nodeRows.Scan(&n.ID, &n.Type, &n.Title, &n.Summary); err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, n)
		live[n.ID] = true
	}

	edgeRows, err := s.db.Query(
		`SELECT from_id, to_id, relation FROM knowledge_edges WHERE tenant_id = ?`,
		tenantID,
	)
	if err != nil {
		return nodes, nil, err
	}
	defer edgeRows.Close()
	var edges []KnowledgeEdge
	for edgeRows.Next() {
		var e KnowledgeEdge
		if err := edgeRows.Scan(&e.FromID, &e.ToID, &e.Relation); err != nil {
			return nodes, nil, err
		}
		// Kun kanter der begge endene er aksepterte.
		if live[e.FromID] && live[e.ToID] {
			edges = append(edges, e)
		}
	}
	return nodes, edges, edgeRows.Err()
}

var errNoNode = errors.New("ingen node")

// NodeByTitle finner en akseptert/pending node ved tittel (for relasjonskobling).
func (s *Store) NodeByTitle(tenantID, title string) (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM knowledge_nodes WHERE tenant_id = ? AND title = ? LIMIT 1`,
		tenantID, title,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNoNode
	}
	return id, err
}
