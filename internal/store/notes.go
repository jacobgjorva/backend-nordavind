package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// KnowledgeNote er én hentbar kunnskaps-lapp. Alt AI-en «vet» ligger som
// lapper i én skuff: enten et faktum brukeren lærte den i chat
// (source_type=fact) eller en bit av et opplastet dokument
// (source_type=document, source_id peker på dokumentet). Hentingen behandler
// dem likt — forskjellen er bare hvordan de kom inn.
type KnowledgeNote struct {
	ID         string    `json:"id"`
	SourceType string    `json:"source_type"` // fact | document
	SourceID   string    `json:"source_id,omitempty"`
	Title      string    `json:"title,omitempty"`   // kort etikett / dokumenttittel
	Text       string    `json:"text"`              // selve lappen (proposisjon / bit)
	Context    string    `json:"context,omitempty"` // kontekst-blurb for dokument-lapper
	Status     string    `json:"status"`            // pending | accepted | rejected
	ChatID     string    `json:"chat_id,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Embedding  []float32 `json:"-"`
}

func (s *Store) migrateNotes() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS knowledge_notes (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			source_type TEXT NOT NULL DEFAULT 'fact',
			source_id   TEXT NOT NULL DEFAULT '',
			title       TEXT NOT NULL DEFAULT '',
			text        TEXT NOT NULL,
			context     TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'pending',
			chat_id     TEXT NOT NULL DEFAULT '',
			user_id     TEXT NOT NULL DEFAULT '',
			embedding   TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_notes_tenant ON knowledge_notes (tenant_id, status);
		CREATE INDEX IF NOT EXISTS idx_notes_source ON knowledge_notes (source_id);
		-- Nøkkelord-søk (BM25) via FTS5. note_id kobler treff tilbake til lappen;
		-- vi holder FTS synkronisert eksplisitt ved insert/delete.
		CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_notes_fts USING fts5(
			note_id UNINDEXED, text, context, tokenize='unicode61'
		);
	`); err != nil {
		return err
	}
	return s.backfillNotes()
}

// backfillNotes flytter eksisterende kunnskap inn i den felles skuffen én gang:
// kuraterte fakta-noder og dokument-biter blir alle til lapper. Kildetabellene
// (knowledge_nodes, document_chunks, documents) beholdes uendret.
func (s *Store) backfillNotes() error {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM knowledge_notes`).Scan(&n); err != nil || n > 0 {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Fakta-noder (alt som ikke er en dokument-node) → fact-lapper. Text =
	// selve faktaet (summary), context = tittel så nøkkelordsøk dekker begge.
	if _, err := tx.Exec(`
		INSERT INTO knowledge_notes
			(id, tenant_id, source_type, source_id, title, text, context, status, chat_id, user_id, embedding, created_at)
		SELECT id, tenant_id, 'fact', '', title, summary, title,
		       status, chat_id, user_id, embedding, created_at
		FROM knowledge_nodes WHERE type != 'dokument'
	`); err != nil {
		return err
	}

	// Dokument-biter → document-lapper, med dokumentets tittel og id.
	if _, err := tx.Exec(`
		INSERT INTO knowledge_notes
			(id, tenant_id, source_type, source_id, title, text, status, embedding, created_at)
		SELECT c.id, c.tenant_id, 'document', c.node_id, n.title, c.content, 'accepted', c.embedding, c.created_at
		FROM document_chunks c JOIN knowledge_nodes n ON n.id = c.node_id
	`); err != nil {
		return err
	}

	// Speil alt til FTS-indeksen.
	if _, err := tx.Exec(`
		INSERT INTO knowledge_notes_fts (note_id, text, context)
		SELECT id, text, context FROM knowledge_notes
	`); err != nil {
		return err
	}
	return tx.Commit()
}

// AcceptedNotes henter alle aksepterte lapper for en tenant med embeddings,
// til vektor-ranking i hentingen.
func (s *Store) AcceptedNotes(tenantID string) ([]KnowledgeNote, error) {
	rows, err := s.db.Query(
		`SELECT id, source_type, source_id, title, text, context, embedding, created_at
		 FROM knowledge_notes WHERE tenant_id = ? AND status = 'accepted'`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KnowledgeNote
	for rows.Next() {
		var n KnowledgeNote
		var emb string
		if err := rows.Scan(&n.ID, &n.SourceType, &n.SourceID, &n.Title, &n.Text, &n.Context, &emb, &n.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(emb), &n.Embedding)
		out = append(out, n)
	}
	return out, rows.Err()
}

// SearchNotesFTS gjør nøkkelord-søk (BM25) og returnerer note-IDer i
// rangert rekkefølge (best først), kun aksepterte lapper for tenanten.
func (s *Store) SearchNotesFTS(tenantID, match string, limit int) ([]string, error) {
	if match == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT n.id
		 FROM knowledge_notes_fts f
		 JOIN knowledge_notes n ON n.id = f.note_id
		 WHERE f.knowledge_notes_fts MATCH ? AND n.tenant_id = ? AND n.status = 'accepted'
		 ORDER BY bm25(f.knowledge_notes_fts) LIMIT ?`,
		match, tenantID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// insertNote skriver en lapp og speiler den til FTS, i samme transaksjon.
func insertNote(tx *sql.Tx, tenantID string, note KnowledgeNote) error {
	emb, _ := json.Marshal(note.Embedding)
	if _, err := tx.Exec(
		`INSERT INTO knowledge_notes
			(id, tenant_id, source_type, source_id, title, text, context, status, chat_id, user_id, embedding, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		note.ID, tenantID, note.SourceType, note.SourceID, note.Title, note.Text, note.Context,
		note.Status, note.ChatID, note.UserID, string(emb), note.CreatedAt,
	); err != nil {
		return err
	}
	_, err := tx.Exec(
		`INSERT INTO knowledge_notes_fts (note_id, text, context) VALUES (?, ?, ?)`,
		note.ID, note.Text, note.Context,
	)
	return err
}

// DocumentInput er metadata for et opplastet dokument som lagres sammen med
// lappene sine.
type DocumentInput struct {
	Filename string
	RawText  string
	Title    string
	Summary  string
}

// CreateDocumentNotes lagrer et dokument som lapper i den felles skuffen, i én
// transaksjon: erstatter et tidligere dokument med samme filnavn (ferskhet),
// lagrer provenance (filnavn + råtekst) og setter inn hver lapp + FTS.
// Returnerer dokumentets id.
func (s *Store) CreateDocumentNotes(tenantID string, doc DocumentInput, notes []KnowledgeNote) (string, error) {
	docID, err := newID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Ferskhet: samme filnavn lastet opp på nytt erstatter det gamle helt.
	if doc.Filename != "" {
		rows, err := tx.Query(
			`SELECT node_id FROM documents WHERE tenant_id = ? AND filename = ?`,
			tenantID, doc.Filename,
		)
		if err != nil {
			return "", err
		}
		var oldIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return "", err
			}
			oldIDs = append(oldIDs, id)
		}
		rows.Close()
		for _, id := range oldIDs {
			if err := deleteNotesBySource(tx, tenantID, id); err != nil {
				return "", err
			}
			if _, err := tx.Exec(`DELETE FROM documents WHERE node_id = ?`, id); err != nil {
				return "", err
			}
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO documents (node_id, tenant_id, filename, raw_text, title) VALUES (?, ?, ?, ?, ?)`,
		docID, tenantID, doc.Filename, doc.RawText, doc.Title,
	); err != nil {
		return "", err
	}
	for _, n := range notes {
		id, err := newID()
		if err != nil {
			return "", err
		}
		n.ID = id
		n.SourceType = "document"
		n.SourceID = docID
		n.Title = doc.Title
		n.Status = "accepted"
		n.CreatedAt = time.Now()
		if err := insertNote(tx, tenantID, n); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return docID, nil
}

// SyncFactNote speiler en akseptert fakta-node inn i lappe-skuffen (retrieval-
// indeksen). Idempotent: erstatter en eksisterende lapp med samme id.
func (s *Store) SyncFactNote(tenantID, id, title, text string, embedding []float32) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := removeNote(tx, id); err != nil {
		return err
	}
	if err := insertNote(tx, tenantID, KnowledgeNote{
		ID: id, SourceType: "fact", Title: title, Text: text, Context: title,
		Status: "accepted", Embedding: embedding, CreatedAt: time.Now(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveNote fjerner en lapp (og FTS-raden) — brukes når en fakta-node
// avvises eller slettes.
func (s *Store) RemoveNote(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := removeNote(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// removeNote sletter en lapp og FTS-raden i en transaksjon.
func removeNote(tx *sql.Tx, id string) error {
	if _, err := tx.Exec(`DELETE FROM knowledge_notes_fts WHERE note_id = ?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM knowledge_notes WHERE id = ?`, id)
	return err
}

// deleteNotesBySource fjerner alle lapper fra en kilde (f.eks. et dokument som
// lastes opp på nytt) og rydder FTS. Kalles i en transaksjon.
func deleteNotesBySource(tx *sql.Tx, tenantID, sourceID string) error {
	if _, err := tx.Exec(
		`DELETE FROM knowledge_notes_fts WHERE note_id IN
			(SELECT id FROM knowledge_notes WHERE tenant_id = ? AND source_id = ?)`,
		tenantID, sourceID,
	); err != nil {
		return err
	}
	_, err := tx.Exec(
		`DELETE FROM knowledge_notes WHERE tenant_id = ? AND source_id = ?`,
		tenantID, sourceID,
	)
	return err
}
