package store

import (
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// KnowledgeNote er én hentbar kunnskaps-lapp. Alt AI-en «vet» ligger som
// lapper i én skuff: enten et faktum brukeren lærte den i chat
// (source_type=fact) eller en bit av et opplastet dokument
// (source_type=document, source_id peker på dokumentet). Hentingen behandler
// dem likt — forskjellen er bare hvordan de kom inn.
type KnowledgeNote struct {
	ID         string    `json:"id"`
	SourceType string    `json:"source_type"` // fact | document | procedure
	SourceID   string    `json:"source_id,omitempty"`
	Title      string    `json:"title,omitempty"`   // kort etikett / dokumenttittel
	Text       string    `json:"text"`              // selve lappen (proposisjon / bit)
	Context    string    `json:"context,omitempty"` // kontekst-blurb for dokument-lapper
	Scope      string    `json:"scope,omitempty"`   // '' tenant | unit:<id> | user:<id> (notescope.go)
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
	// Bruks-telling (governance v2): hvor ofte en lapp faktisk hentes —
	// grunnlaget for falming og selvkuraterende opprydding. Kolonnene legges
	// til idempotent (feiler stille når de alt finnes).
	s.db.Exec(`ALTER TABLE knowledge_notes ADD COLUMN hits INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE knowledge_notes ADD COLUMN last_hit_at TIMESTAMP`)
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
		var emb sql.NullString
		if err := rows.Scan(&n.ID, &n.SourceType, &n.SourceID, &n.Title, &n.Text, &n.Context, &emb, &n.CreatedAt); err != nil {
			return nil, err
		}
		if emb.Valid {
			json.Unmarshal([]byte(emb.String), &n.Embedding)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SearchNotesFTS gjør nøkkelord-søk og returnerer note-IDer i rangert
// rekkefølge (best først), kun aksepterte lapper for tenanten. Tar rene
// innholdsord (allerede stoppord-filtrert) — dialekten bygger spørringen:
// SQLite FTS5/BM25, Postgres tsquery/ts_rank.
func (s *Store) SearchNotesFTS(tenantID string, tokens []string, limit int) ([]string, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	var rows *sql.Rows
	var err error
	if s.db.pg {
		// to_tsquery med OR — tokens er ord-tegn (regex-plukket), trygge.
		rows, err = s.db.Query(
			`SELECT id FROM knowledge_notes
			 WHERE tenant_id = ? AND status = 'accepted'
			   AND fts @@ to_tsquery('norwegian', ?)
			 ORDER BY ts_rank(fts, to_tsquery('norwegian', ?)) DESC LIMIT ?`,
			tenantID, strings.Join(tokens, " | "), strings.Join(tokens, " | "), limit,
		)
	} else {
		quoted := make([]string, len(tokens))
		for i, t := range tokens {
			quoted[i] = `"` + t + `"`
		}
		rows, err = s.db.Query(
			`SELECT n.id
			 FROM knowledge_notes_fts f
			 JOIN knowledge_notes n ON n.id = f.note_id
			 WHERE f.knowledge_notes_fts MATCH ? AND n.tenant_id = ? AND n.status = 'accepted'
			 ORDER BY bm25(f.knowledge_notes_fts) LIMIT ?`,
			strings.Join(quoted, " OR "), tenantID, limit,
		)
	}
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

// SearchNotesSubstring finner lapper der et søkeord står INNE i et ord.
// Norsk skriver sammensatt: «møte» finnes ikke som eget token i
// «Mandagsmøte», så ren ordmatching bommer på nettopp de spørsmålene folk
// stiller. Kjører lokalt i basen — ingen nettverkskall, ingen tokens.
func (s *Store) SearchNotesSubstring(tenantID string, tokens []string, limit int) ([]string, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	// Bare ord med litt tyngde: korte biter treffer alt.
	var terms []string
	for _, t := range tokens {
		if len([]rune(t)) >= 4 {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}
	where := make([]string, len(terms))
	args := []any{tenantID}
	for i, t := range terms {
		// lower() på begge sider: virker likt i SQLite og Postgres.
		where[i] = "(lower(text) LIKE ? OR lower(context) LIKE ?)"
		low := strings.ToLower(t)
		args = append(args, "%"+low+"%", "%"+low+"%")
	}
	args = append(args, limit)
	rows, err := s.db.Query(
		`SELECT id FROM knowledge_notes
		 WHERE tenant_id = ? AND status = 'accepted' AND (`+strings.Join(where, " OR ")+`)
		 ORDER BY created_at DESC LIMIT ?`, args...)
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

// TopNotesByEmbedding er pgvector-hentingen: topp-k aksepterte lapper etter
// cosinelikhet, med likheten returnert. Kun Postgres — SQLite-stien laster
// alle lapper og regner i Go (AcceptedNotes).
func (s *Store) TopNotesByEmbedding(tenantID string, vec []float32, k int) ([]KnowledgeNote, []float64, error) {
	q, _ := json.Marshal(vec)
	rows, err := s.db.Query(
		`SELECT id, source_type, source_id, title, text, context, created_at,
		        1 - (embedding <=> ?::vector) AS sim
		 FROM knowledge_notes
		 WHERE tenant_id = ? AND status = 'accepted' AND embedding IS NOT NULL
		 ORDER BY embedding <=> ?::vector LIMIT ?`,
		string(q), tenantID, string(q), k,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var notes []KnowledgeNote
	var sims []float64
	for rows.Next() {
		var n KnowledgeNote
		var sim float64
		if err := rows.Scan(&n.ID, &n.SourceType, &n.SourceID, &n.Title, &n.Text, &n.Context, &n.CreatedAt, &sim); err != nil {
			return nil, nil, err
		}
		notes = append(notes, n)
		sims = append(sims, sim)
	}
	return notes, sims, rows.Err()
}

// NotesByIDs henter aksepterte lapper på id (til FTS-treff og kant-naboer i
// Postgres-stien). Rekkefølgen er ikke garantert.
func (s *Store) NotesByIDs(tenantID string, ids []string) ([]KnowledgeNote, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, tenantID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(
		`SELECT id, source_type, source_id, title, text, context, created_at
		 FROM knowledge_notes
		 WHERE tenant_id = ? AND status = 'accepted' AND id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeNote
	for rows.Next() {
		var n KnowledgeNote
		if err := rows.Scan(&n.ID, &n.SourceType, &n.SourceID, &n.Title, &n.Text, &n.Context, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// insertNote skriver en lapp og holder søkeindeksen i synk, i samme
// transaksjon: SQLite speiler til FTS5-tabellen, Postgres setter tsvector- og
// vector-kolonnene direkte.
func insertNote(tx *Tx, tenantID string, note KnowledgeNote) error {
	if tx.pg {
		var emb any
		if len(note.Embedding) > 0 {
			b, _ := json.Marshal(note.Embedding)
			emb = string(b)
		}
		_, err := tx.Exec(
			`INSERT INTO knowledge_notes
				(id, tenant_id, source_type, source_id, title, text, context, status, chat_id, user_id, scope, embedding, fts, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::vector, to_tsvector('norwegian', ? || ' ' || ?), ?)`,
			note.ID, tenantID, note.SourceType, note.SourceID, note.Title, note.Text, note.Context,
			note.Status, note.ChatID, note.UserID, note.Scope, emb, note.Text, note.Context, note.CreatedAt,
		)
		return err
	}
	emb, _ := json.Marshal(note.Embedding)
	if _, err := tx.Exec(
		`INSERT INTO knowledge_notes
			(id, tenant_id, source_type, source_id, title, text, context, status, chat_id, user_id, scope, embedding, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		note.ID, tenantID, note.SourceType, note.SourceID, note.Title, note.Text, note.Context,
		note.Status, note.ChatID, note.UserID, note.Scope, string(emb), note.CreatedAt,
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
	// Scope arves av alle lappene dokumentet gir (KUNNSKAP-V2 del 3):
	// default opplasterens enhet, kan heves til tenant ('') ved opplasting.
	Scope string
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
		if n.SourceType == "" {
			n.SourceType = "document"
		}
		n.SourceID = docID
		if n.Title == "" {
			n.Title = doc.Title
		}
		n.Scope = doc.Scope
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
func removeNote(tx *Tx, id string) error {
	if !tx.pg {
		if _, err := tx.Exec(`DELETE FROM knowledge_notes_fts WHERE note_id = ?`, id); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`DELETE FROM knowledge_notes WHERE id = ?`, id)
	return err
}

// deleteNotesBySource fjerner alle lapper fra en kilde (f.eks. et dokument som
// lastes opp på nytt) og rydder FTS. Kalles i en transaksjon.
func deleteNotesBySource(tx *Tx, tenantID, sourceID string) error {
	if !tx.pg {
		if _, err := tx.Exec(
			`DELETE FROM knowledge_notes_fts WHERE note_id IN
				(SELECT id FROM knowledge_notes WHERE tenant_id = ? AND source_id = ?)`,
			tenantID, sourceID,
		); err != nil {
			return err
		}
	}
	_, err := tx.Exec(
		`DELETE FROM knowledge_notes WHERE tenant_id = ? AND source_id = ?`,
		tenantID, sourceID,
	)
	return err
}

// MostSimilarAcceptedFact finner den mest lignende aksepterte fakta-lappen
// (dublettvakten, G5): Postgres spør pgvector, SQLite regner cosine i Go.
// Returnerer ErrNotFound når skuffen er tom.
func (s *Store) MostSimilarAcceptedFact(tenantID string, vec []float32, excludeID string) (KnowledgeNote, float64, error) {
	if s.db.pg {
		q, _ := json.Marshal(vec)
		var n KnowledgeNote
		var sim float64
		err := s.db.QueryRow(
			`SELECT id, title, text, 1 - (embedding <=> ?::vector)
			 FROM knowledge_notes
			 WHERE tenant_id = ? AND status = 'accepted' AND source_type = 'fact'
			   AND embedding IS NOT NULL AND id != ?
			 ORDER BY embedding <=> ?::vector LIMIT 1`,
			string(q), tenantID, excludeID, string(q),
		).Scan(&n.ID, &n.Title, &n.Text, &sim)
		if err == sql.ErrNoRows {
			return n, 0, ErrNotFound
		}
		return n, sim, err
	}
	notes, err := s.AcceptedNotes(tenantID)
	if err != nil {
		return KnowledgeNote{}, 0, err
	}
	best := KnowledgeNote{}
	bestSim := -1.0
	for _, n := range notes {
		if n.SourceType != "fact" || n.ID == excludeID || len(n.Embedding) == 0 {
			continue
		}
		if c := cosine32(vec, n.Embedding); c > bestSim {
			bestSim = c
			best = n
		}
	}
	if bestSim < 0 {
		return best, 0, ErrNotFound
	}
	return best, bestSim, nil
}

// cosine32 er cosinelikheten mellom to vektorer (SQLite-stien til vakten).
func cosine32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// RecordNoteHits teller at lappene ble hentet inn i en svar-kontekst —
// grunnlaget for bruksbasert kuratering. Best effort, aldri i kritisk sti.
func (s *Store) RecordNoteHits(ids []string) {
	if len(ids) == 0 {
		return
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, time.Now())
	for _, id := range ids {
		args = append(args, id)
	}
	s.db.Exec(`UPDATE knowledge_notes SET hits = hits + 1, last_hit_at = ? WHERE id IN (`+ph+`)`, args...)
}

// AddProcedureNote lagrer en uttrukket prosedyre som hentbar lapp
// (KUNNSKAP-V2 del 3): source_type=procedure, dokumentets scope, kobles til
// dok-noden med kant av kalleren.
func (s *Store) AddProcedureNote(tenantID, docID, name, body, scope string, embedding []float32) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	note := KnowledgeNote{
		ID: id, SourceType: "procedure", SourceID: docID, Title: name,
		Text: body, Scope: scope, Status: "accepted", CreatedAt: time.Now(),
		Embedding: embedding,
	}
	if err := insertNote(tx, tenantID, note); err != nil {
		return "", err
	}
	return id, tx.Commit()
}
