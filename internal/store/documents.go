package store

import (
	"encoding/json"
	"time"
)

// DocumentChunk er ett utsnitt av et opplastet dokument. Bitene ligger under
// en doknode (en knowledge_node av type "dokument"): noden gir graf-visning og
// sletting, mens bitene bærer selve teksten og embeddingen som hentingen bruker.
type DocumentChunk struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"` // doknoden i knowledge_nodes
	Ordinal   int       `json:"ordinal"` // rekkefølge i dokumentet
	Content   string    `json:"content"`
	DocTitle  string    `json:"doc_title,omitempty"` // fylt ved henting (til kildehenvisning)
	CreatedAt time.Time `json:"created_at"`
	Embedding []float32 `json:"-"`
}

func (s *Store) migrateDocuments() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS document_chunks (
			id         TEXT PRIMARY KEY,
			node_id    TEXT NOT NULL,
			tenant_id  TEXT NOT NULL,
			ordinal    INTEGER NOT NULL DEFAULT 0,
			content    TEXT NOT NULL,
			embedding  TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_dchunks_node ON document_chunks (node_id);
		CREATE INDEX IF NOT EXISTS idx_dchunks_tenant ON document_chunks (tenant_id);
		CREATE TABLE IF NOT EXISTS documents (
			node_id    TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			filename   TEXT NOT NULL DEFAULT '',
			raw_text   TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return err
	}
	// Tittel til bibliotek-visningen (lagt til i etterkant).
	s.db.Exec(`ALTER TABLE documents ADD COLUMN title TEXT NOT NULL DEFAULT ''`)
	return nil
}

// Document er ett opplastet dokument til bibliotek-listen.
type Document struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Filename  string    `json:"filename"`
	Notes     int       `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
}

// ListDocuments returnerer tenantens opplastede dokumenter, nyest først, med
// antall lapper hver har.
func (s *Store) ListDocuments(tenantID string) ([]Document, error) {
	rows, err := s.db.Query(
		`SELECT d.node_id, d.title, d.filename, d.created_at,
		        (SELECT count(*) FROM knowledge_notes n WHERE n.source_id = d.node_id)
		 FROM documents d WHERE d.tenant_id = ? ORDER BY d.created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Filename, &d.CreatedAt, &d.Notes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDocument fjerner et dokument, alle lappene og FTS-radene, i én
// transaksjon.
func (s *Store) DeleteDocument(tenantID, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM documents WHERE node_id = ? AND tenant_id = ?`, id, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := deleteNotesBySource(tx, tenantID, id); err != nil {
		return err
	}
	// Rydd grafen: dok-noden og kantene dens forsvinner med dokumentet.
	// Uttrukne prosedyre/regel-noder består — de er godkjent kunnskap i seg
	// selv og eies ikke av kilden.
	if _, err := tx.Exec(`DELETE FROM knowledge_nodes WHERE id = ? AND tenant_id = ?`, id, tenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM knowledge_edges WHERE tenant_id = ? AND (from_id = ? OR to_id = ?)`, tenantID, id, id); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateDocument lagrer et opplastet dokument som én accepted doknode, en rad
// med provenance (filnavn + råtekst som fallback), og de propositionaliserte
// bitene under noden — alt i én transaksjon. Returnerer noden (med ID).
func (s *Store) CreateDocument(tenantID, filename, rawText string, node KnowledgeNode, chunks []DocumentChunk) (KnowledgeNode, error) {
	nodeID, err := newID()
	if err != nil {
		return node, err
	}
	node.ID = nodeID
	node.CreatedAt = time.Now()
	if node.Type == "" {
		node.Type = "dokument"
	}
	if node.Status == "" {
		node.Status = "accepted"
	}

	tx, err := s.db.Begin()
	if err != nil {
		return node, err
	}
	defer tx.Rollback()

	emb, _ := json.Marshal(node.Embedding)
	if _, err := tx.Exec(
		`INSERT INTO knowledge_nodes (id, tenant_id, type, title, summary, status, chat_id, user_id, embedding)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ID, tenantID, node.Type, node.Title, node.Summary, node.Status, node.ChatID, node.UserID, string(emb),
	); err != nil {
		return node, err
	}

	if _, err := tx.Exec(
		`INSERT INTO documents (node_id, tenant_id, filename, raw_text) VALUES (?, ?, ?, ?)`,
		node.ID, tenantID, filename, rawText,
	); err != nil {
		return node, err
	}

	for i, c := range chunks {
		chunkID, err := newID()
		if err != nil {
			return node, err
		}
		cemb, _ := json.Marshal(c.Embedding)
		if _, err := tx.Exec(
			`INSERT INTO document_chunks (id, node_id, tenant_id, ordinal, content, embedding)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			chunkID, node.ID, tenantID, i, c.Content, string(cemb),
		); err != nil {
			return node, err
		}
	}
	return node, tx.Commit()
}

// AcceptedChunks henter alle dokument-biter for en tenant med embeddings og
// dokumentets tittel, til ranking i knowledgeContext.
func (s *Store) AcceptedChunks(tenantID string) ([]DocumentChunk, error) {
	rows, err := s.db.Query(
		`SELECT c.id, c.node_id, c.ordinal, c.content, c.embedding, n.title
		 FROM document_chunks c
		 JOIN knowledge_nodes n ON n.id = c.node_id
		 WHERE c.tenant_id = ? AND n.status = 'accepted'`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DocumentChunk
	for rows.Next() {
		var c DocumentChunk
		var emb string
		if err := rows.Scan(&c.ID, &c.NodeID, &c.Ordinal, &c.Content, &emb, &c.DocTitle); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(emb), &c.Embedding)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteDocumentData fjerner biter og provenance-rad under en doknode. Kalles
// fra DeleteNode så sletting av dokumentet i grafen rydder alt tilhørende.
func (s *Store) DeleteDocumentData(nodeID string) error {
	if _, err := s.db.Exec(`DELETE FROM document_chunks WHERE node_id = ?`, nodeID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM documents WHERE node_id = ?`, nodeID)
	return err
}
