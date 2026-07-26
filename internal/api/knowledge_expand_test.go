package api

import (
	"path/filepath"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// G2: en dokument-bit i topptreffene skal dra inn noder som er koblet til
// dokumentet sitt med kant — og aldri noder uten lapp eller alt i treffene.
func TestExpandNeighborsViaDocEdge(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}

	docID, err := st.CreateDocumentNotes("t1", store.DocumentInput{Filename: "rutine.pdf", Title: "Rutine"},
		[]store.KnowledgeNote{{Text: "bit om kjølevarer", Embedding: []float32{1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncFactNote("t1", "node-1", "Fryseromskode", "koden er 4412", []float32{1}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEdge("t1", store.KnowledgeEdge{FromID: "node-1", ToID: docID, Relation: "definert i"}); err != nil {
		t.Fatal(err)
	}

	notes, err := st.AcceptedNotes("t1")
	if err != nil || len(notes) != 2 {
		t.Fatalf("ventet 2 lapper, fikk %d (%v)", len(notes), err)
	}
	byID := map[string]store.KnowledgeNote{}
	var chunkID string
	for _, n := range notes {
		byID[n.ID] = n
		if n.SourceType == "document" {
			chunkID = n.ID
		}
	}

	got := s.expandNeighbors("t1", []string{chunkID}, byID)
	if len(got) != 1 || got[0] != "node-1" {
		t.Fatalf("ventet [node-1] via dok-kanten, fikk %v", got)
	}
	// Noden alt i treffene: ingenting å utvide.
	if got := s.expandNeighbors("t1", []string{chunkID, "node-1"}, byID); len(got) != 0 {
		t.Fatalf("nabo som alt er i treffene skal ikke dupliseres, fikk %v", got)
	}
}
