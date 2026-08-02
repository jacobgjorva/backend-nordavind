package store

import (
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
	// Idempotente kolonnetillegg for M365-synken (eier + opphav).
	for _, stmt := range []string{
		`ALTER TABLE documents ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE documents ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE documents ADD COLUMN origin_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE documents ADD COLUMN origin_mod TEXT NOT NULL DEFAULT ''`,
	} {
		s.db.Exec(stmt)
	}
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
			title      TEXT NOT NULL DEFAULT '',
			owner      TEXT NOT NULL DEFAULT '',
			origin_id  TEXT NOT NULL DEFAULT '',
			origin_mod TEXT NOT NULL DEFAULT '',
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

// DeleteDocumentData fjerner biter og provenance-rad under en doknode. Kalles
// fra DeleteNode så sletting av dokumentet i grafen rydder alt tilhørende.
func (s *Store) DeleteDocumentData(nodeID string) error {
	if _, err := s.db.Exec(`DELETE FROM document_chunks WHERE node_id = ?`, nodeID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM documents WHERE node_id = ?`, nodeID)
	return err
}
