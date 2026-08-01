package store

import (
	"encoding/json"
	"strings"
)

// Kunnskaps-scope (KUNNSKAP-V2 del 2). Én kolonne, én klausul, ett sted:
// hver lapp bærer et scope, og ALL henting går gjennom scopeClause. Aldri
// synlighetsfiltrering i kallsteder eller prompter.
//
// Verdiene: ”         tenant-vid (default — null org-data = alt synlig)
//
//	'unit:<id>' kun brukere i enheten
//	'user:<id>' privat for én bruker
const (
	ScopeTenant = ""
)

func UnitScope(unitID string) string { return "unit:" + unitID }
func UserScope(userID string) string { return "user:" + userID }

func (s *Store) migrateNoteScope() error {
	// Idempotent i SQLite (duplikatfeil ignoreres); Postgres har IF NOT
	// EXISTS i pgschema.sql.
	s.db.Exec(`ALTER TABLE knowledge_notes ADD COLUMN scope TEXT NOT NULL DEFAULT ''`)
	return nil
}

// scopeClause er DEN ENE synlighetsklausulen. Brukeren ser tenant-vid
// kunnskap, sin egen enhets, og sin private. prefix er kolonneprefiks
// («n.») for spørringer med join. args returneres for binding.
func scopeClause(prefix string, sc Scope, userID string) (string, []any) {
	col := prefix + "scope"
	clauses := []string{col + " = ''"}
	args := []any{}
	for _, uid := range sc.UnitIDs {
		clauses = append(clauses, col+" = ?")
		args = append(args, UnitScope(uid))
	}
	if userID != "" {
		clauses = append(clauses, col+" = ?")
		args = append(args, UserScope(userID))
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

// ScopedNote er en kandidat til relevans-porten: lappen + vektorlikheten
// der den finnes (SQLite-stien regner cosine i Go inntil pgvector-stien er
// eneste — da har raden sim fra basen).
type ScopedNote struct {
	ID        string
	Title     string
	Text      string
	Context   string
	Source    string // source_type: fact | document
	SourceID  string // dokument-id for dokument-biter (kantutvidelsens frø)
	Embedding []float32
	Sim       float64
}

// ScopedVectorCandidates henter de k nærmeste lappene brukeren HAR LOV å
// se. Postgres: pgvector-indeks. SQLite (dev/eval): scope-filtrert last +
// cosine i Go — samme semantikk, målt av samme eval.
func (s *Store) ScopedVectorCandidates(tenantID string, sc Scope, userID string, vec []float32, k int) ([]ScopedNote, error) {
	clause, scopeArgs := scopeClause("", sc, userID)
	if s.db.pg {
		q, _ := json.Marshal(vec)
		args := append([]any{string(q), tenantID}, scopeArgs...)
		args = append(args, string(q), k)
		rows, err := s.db.Query(
			`SELECT id, title, text, context, source_type,
			        1 - (embedding <=> ?::vector) AS sim
			 FROM knowledge_notes
			 WHERE tenant_id = ? AND status = 'accepted' AND embedding IS NOT NULL
			   AND `+clause+`
			 ORDER BY embedding <=> ?::vector LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []ScopedNote
		for rows.Next() {
			var n ScopedNote
			if err := rows.Scan(&n.ID, &n.Title, &n.Text, &n.Context, &n.Source, &n.SourceID, &n.Sim); err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, rows.Err()
	}
	args := append([]any{tenantID}, scopeArgs...)
	rows, err := s.db.Query(
		`SELECT id, title, text, context, source_type, source_id, embedding
		 FROM knowledge_notes
		 WHERE tenant_id = ? AND status = 'accepted' AND embedding IS NOT NULL
		   AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopedNote
	for rows.Next() {
		var n ScopedNote
		var raw []byte
		if err := rows.Scan(&n.ID, &n.Title, &n.Text, &n.Context, &n.Source, &n.SourceID, &raw); err != nil {
			return nil, err
		}
		json.Unmarshal(raw, &n.Embedding)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Sim = cosine32(vec, out[i].Embedding)
		out[i].Embedding = nil
	}
	sortBySim(out)
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// ScopedKeywordCandidates er FTS-benet, med samme klausul.
func (s *Store) ScopedKeywordCandidates(tenantID string, sc Scope, userID string, tokens []string, limit int) ([]ScopedNote, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	clause, scopeArgs := scopeClause("", sc, userID)
	if s.db.pg {
		args := append([]any{strings.Join(tokens, " | "), tenantID}, scopeArgs...)
		args = append(args, limit)
		rows, err := s.db.Query(
			`SELECT id, title, text, context, source_type, source_id, 0
			 FROM knowledge_notes
			 WHERE fts @@ to_tsquery('norwegian', ?)
			   AND tenant_id = ? AND status = 'accepted' AND `+clause+`
			 LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanScoped(rows)
	}
	njoin, njoinArgs := scopeClause("n.", sc, userID)
	args := append([]any{strings.Join(tokens, " OR "), tenantID}, njoinArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(
		`SELECT n.id, n.title, n.text, n.context, n.source_type, n.source_id, 0
		 FROM knowledge_notes_fts f
		 JOIN knowledge_notes n ON n.id = f.note_id
		 WHERE knowledge_notes_fts MATCH ?
		   AND n.tenant_id = ? AND n.status = 'accepted' AND `+njoin+`
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScoped(rows)
}

func scanScoped(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]ScopedNote, error) {
	var out []ScopedNote
	for rows.Next() {
		var n ScopedNote
		if err := rows.Scan(&n.ID, &n.Title, &n.Text, &n.Context, &n.Source, &n.SourceID, &n.Sim); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetNoteScope endrer synligheten på en lapp (brukes av inngest i del 3 og
// admin-flatene i del 4).
func (s *Store) SetNoteScope(tenantID, noteID, scope string) error {
	res, err := s.db.Exec(
		`UPDATE knowledge_notes SET scope = ? WHERE id = ? AND tenant_id = ?`,
		scope, noteID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func sortBySim(ns []ScopedNote) {
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && ns[j].Sim > ns[j-1].Sim; j-- {
			ns[j], ns[j-1] = ns[j-1], ns[j]
		}
	}
}

// ScopedNeighbors er kantutvidelsen (G2 i v2, BEGGE retninger): for valgte
// kandidater hentes kant-naboene som brukeren har lov å se. Frø: dokument-
// biter peker på dok-noden (source_id), fakta-lapper på seg selv. Naboer:
// fakta-lapper med id i kantens andre ende, OG dokument-biter til en dok-id
// i kantens andre ende — det siste var garantert null i v1.
func (s *Store) ScopedNeighbors(tenantID string, sc Scope, userID string, seeds []string, limit int) ([]ScopedNote, error) {
	if len(seeds) == 0 || limit <= 0 {
		return nil, nil
	}
	edges, err := s.EdgesTouching(tenantID, seeds)
	if err != nil || len(edges) == 0 {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range seeds {
		seen[id] = true
	}
	var others []string
	for _, e := range edges {
		for _, id := range []string{e.FromID, e.ToID} {
			if !seen[id] {
				seen[id] = true
				others = append(others, id)
			}
		}
	}
	if len(others) == 0 {
		return nil, nil
	}
	clause, scopeArgs := scopeClause("", sc, userID)
	ph := strings.TrimRight(strings.Repeat("?,", len(others)), ",")
	args := []any{tenantID}
	for _, id := range others {
		args = append(args, id)
	}
	for _, id := range others {
		args = append(args, id)
	}
	args = append(args, scopeArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(
		`SELECT id, title, text, context, source_type, source_id, 0
		 FROM knowledge_notes
		 WHERE tenant_id = ? AND status = 'accepted'
		   AND (id IN (`+ph+`) OR source_id IN (`+ph+`))
		   AND `+clause+`
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScoped(rows)
}
