package store

import (
	"path/filepath"
	"testing"
)

// LEKKASJETESTEN (KUNNSKAP-V2 del 2): to enheter, begge henteveier, alle
// scope-kombinasjoner. Denne testen er selve garantien bak «selskap A ser
// aldri selskap B» — den skal utvides, aldri svekkes.

func scopeStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedNote(t *testing.T, s *Store, id, text, scope string) {
	t.Helper()
	vec := make([]float32, 8)
	vec[0] = 1 // alle like — testen gjelder synlighet, ikke rangering
	if err := s.SyncFactNote("t1", id, id, text, vec); err != nil {
		t.Fatal(err)
	}
	if scope != "" {
		if err := s.SetNoteScope("t1", id, scope); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScopedRetrievalNeverLeaksAcrossUnits(t *testing.T) {
	s := scopeStore(t)
	seedNote(t, s, "felles", "felles rutine for hele firmaet", "")
	seedNote(t, s, "a-strategi", "selskap A sin strategi", UnitScope("unit-a"))
	seedNote(t, s, "b-resultat", "selskap B sitt driftsresultat", UnitScope("unit-b"))
	seedNote(t, s, "privat-ola", "olas private notat", UserScope("user-ola"))

	q := make([]float32, 8)
	q[0] = 1

	visible := func(sc Scope, userID string) map[string]bool {
		t.Helper()
		got := map[string]bool{}
		vecHits, err := s.ScopedVectorCandidates("t1", sc, userID, q, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range vecHits {
			got[n.ID] = true
		}
		kwHits, err := s.ScopedKeywordCandidates("t1", sc, userID, []string{"selskap", "rutine", "notat", "driftsresultat"}, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range kwHits {
			got[n.ID] = true
		}
		return got
	}

	// Bruker i enhet A: felles + A. ALDRI B, aldri andres private.
	gotA := visible(Scope{UnitID: "unit-a"}, "user-kari")
	for id, want := range map[string]bool{"felles": true, "a-strategi": true, "b-resultat": false, "privat-ola": false} {
		if gotA[id] != want {
			t.Errorf("enhet A ser %q=%v, skulle vært %v", id, gotA[id], want)
		}
	}

	// Bruker i enhet B: felles + B.
	gotB := visible(Scope{UnitID: "unit-b"}, "user-per")
	if gotB["a-strategi"] || !gotB["b-resultat"] {
		t.Errorf("enhet B: %+v", gotB)
	}

	// Uten enhet: kun felles.
	gotNone := visible(Scope{}, "user-ny")
	if gotNone["a-strategi"] || gotNone["b-resultat"] || gotNone["privat-ola"] || !gotNone["felles"] {
		t.Errorf("uten enhet: %+v", gotNone)
	}

	// Ola ser sitt private — Kari gjør det ikke (sjekket over).
	gotOla := visible(Scope{UnitID: "unit-a"}, "user-ola")
	if !gotOla["privat-ola"] {
		t.Errorf("eieren ser ikke sitt private notat: %+v", gotOla)
	}
}

// Del 3: dokument-scope arves av alle lappene, og prosedyre-lappen er
// synlig via både porten (direkte) og kanten (nabo) — men aldri utenfor
// sitt scope.
func TestDocumentScopeInheritanceAndProcedureNeighbors(t *testing.T) {
	s := scopeStore(t)
	vec := make([]float32, 8)
	vec[0] = 1

	docID, err := s.CreateDocumentNotes("t1", DocumentInput{
		Filename: "rutine.pdf", Title: "Kredittrutinen", Scope: UnitScope("unit-a"),
	}, []KnowledgeNote{{Text: "kreditnota krever årsakskode", Embedding: vec}})
	if err != nil {
		t.Fatal(err)
	}
	procID, err := s.AddProcedureNote("t1", docID, "Kreditering",
		"Prosedyre: Kreditering\n  1. Finn faktura\n  2. Opprett kreditnota", UnitScope("unit-a"), vec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEdge("t1", KnowledgeEdge{FromID: procID, ToID: docID, Relation: "definert i"}); err != nil {
		t.Fatal(err)
	}

	// Innenfor enheten: dok-bit synlig, og prosedyren nås som kant-nabo.
	inA, err := s.ScopedVectorCandidates("t1", Scope{UnitID: "unit-a"}, "u1", vec, 10)
	if err != nil || len(inA) != 2 {
		t.Fatalf("enhet A skulle sett dok-bit + prosedyre: %d, err=%v", len(inA), err)
	}
	nbs, err := s.ScopedNeighbors("t1", Scope{UnitID: "unit-a"}, "u1", []string{docID}, 5)
	if err != nil || len(nbs) == 0 {
		t.Fatalf("kant-naboen (prosedyren) mangler: %v", err)
	}

	// Utenfor enheten: ingenting av det, heller ikke via kanten.
	outB, _ := s.ScopedVectorCandidates("t1", Scope{UnitID: "unit-b"}, "u2", vec, 10)
	if len(outB) != 0 {
		t.Fatalf("enhet B ser enhet A sitt dokument: %+v", outB)
	}
	nbsB, _ := s.ScopedNeighbors("t1", Scope{UnitID: "unit-b"}, "u2", []string{docID}, 5)
	if len(nbsB) != 0 {
		t.Fatalf("kantutvidelsen lekker over enhetsgrensen: %+v", nbsB)
	}
}
