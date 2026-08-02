package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// KnowledgeNode er én atomær bransje-fakta i tenantens kunnskapsgraf.
// Status styrer governance: kun "accepted" noder brukes av AI-en.
type KnowledgeNode struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`    // entitet | term | prosess | regel
	Title     string     `json:"title"`   // kort navn
	Summary   string     `json:"summary"` // én linje
	Status    string     `json:"status"`  // pending | accepted | rejected
	ChatID    string     `json:"chat_id,omitempty"`
	UserID    string     `json:"user_id,omitempty"`
	UserEmail string     `json:"user_email,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Hits      int        `json:"hits"`                  // retrieval-treff (fra lappen)
	LastHitAt *time.Time `json:"last_hit_at,omitempty"` // sist hentet
	Embedding []float32  `json:"-"`
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
	id, err := newID()
	if err != nil {
		return n, err
	}
	n.ID = id
	n.CreatedAt = time.Now()
	if n.Status == "" {
		n.Status = "pending"
	}
	if n.Type == "" {
		n.Type = "term"
	}
	emb, _ := json.Marshal(n.Embedding)
	_, err = s.db.Exec(
		`INSERT INTO knowledge_nodes (id, tenant_id, type, title, summary, status, chat_id, user_id, embedding)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, tenantID, n.Type, n.Title, n.Summary, n.Status, n.ChatID, n.UserID, string(emb),
	)
	return n, err
}

// EnsureDocNode oppretter (eller oppdaterer) grafnoden for et opplastet
// dokument. ID deles med documents.node_id, så uttrukne prosedyre/regel-noder
// kan kobles til dokumentet sitt med kanter (G4 i docs/KNOWLEDGE.md).
// Dokumentet er godkjent som helhet ved opplasting → accepted.
func (s *Store) EnsureDocNode(tenantID, docID, title, summary string, embedding []float32) error {
	emb, _ := json.Marshal(embedding)
	_, err := s.db.Exec(
		`INSERT INTO knowledge_nodes (id, tenant_id, type, title, summary, status, embedding)
		 VALUES (?, ?, 'dokument', ?, ?, 'accepted', ?)
		 ON CONFLICT(id) DO UPDATE SET title = excluded.title, summary = excluded.summary,
		   embedding = excluded.embedding`,
		docID, tenantID, title, summary, string(emb),
	)
	return err
}

// AddEdge lagrer en relasjon mellom to noder (idempotent).
func (s *Store) AddEdge(tenantID string, e KnowledgeEdge) error {
	_, err := s.db.Exec(
		`INSERT INTO knowledge_edges (tenant_id, from_id, to_id, relation)
		 VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		tenantID, e.FromID, e.ToID, e.Relation,
	)
	return err
}

// DeleteEdge fjerner én kant (graf-editoren).
func (s *Store) DeleteEdge(tenantID string, e KnowledgeEdge) error {
	_, err := s.db.Exec(
		`DELETE FROM knowledge_edges WHERE tenant_id = ? AND from_id = ? AND to_id = ? AND relation = ?`,
		tenantID, e.FromID, e.ToID, e.Relation,
	)
	return err
}

// EdgesTouching returnerer alle kanter som berører noen av de gitte nodene
// (begge retninger) — 1-hopps nabolag for kant-utvidelsen i hentingen (G2).
func (s *Store) EdgesTouching(tenantID string, ids []string) ([]KnowledgeEdge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, 2*len(ids)+1)
	args = append(args, tenantID)
	for _, id := range ids {
		args = append(args, id)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(
		`SELECT from_id, to_id, relation FROM knowledge_edges
		 WHERE tenant_id = ? AND (from_id IN (`+ph+`) OR to_id IN (`+ph+`))`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeEdge
	for rows.Next() {
		var e KnowledgeEdge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.Relation); err != nil {
			return nil, err
		}
		out = append(out, e)
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
	return s.SyncFactNote(tenantID, id, title, summary, embedding)
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
	// Doknoder har biter + provenance under seg — rydd dem med.
	s.DeleteDocumentData(id)
	// Fjern den speilede lappen fra retrieval-skuffen.
	s.RemoveNote(id)
	return nil
}

// GraphData henter aksepterte noder og kantene mellom dem (til visualisering).
func (s *Store) GraphData(tenantID string) ([]KnowledgeNode, []KnowledgeEdge, error) {
	// Bruken (hits) hentes fra retrieval-skuffen — editoren dimmer lapper som
	// aldri hentes (selvkuraterende kvalitet, governance v2).
	nodeRows, err := s.db.Query(
		`SELECT n.id, n.type, n.title, n.summary, n.created_at,
		        COALESCE(kn.hits, 0), kn.last_hit_at
		 FROM knowledge_nodes n LEFT JOIN knowledge_notes kn ON kn.id = n.id
		 WHERE n.tenant_id = ? AND n.status = 'accepted' AND n.type != 'dokument'`,
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
		if err := nodeRows.Scan(&n.ID, &n.Type, &n.Title, &n.Summary, &n.CreatedAt, &n.Hits, &n.LastHitAt); err != nil {
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
