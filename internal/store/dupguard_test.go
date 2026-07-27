package store

import (
	"path/filepath"
	"testing"
)

// G5: dublettvakten skal peke på den mest lignende aksepterte fakta-lappen
// og aldri sammenligne noden med seg selv.
func TestMostSimilarAcceptedFact(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SyncFactNote("t1", "n1", "Purrerutine", "purres etter 14 dager", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncFactNote("t1", "n2", "Frakt", "fraktfritt over 2500", []float32{0, 1, 0}); err != nil {
		t.Fatal(err)
	}

	// Nesten identisk med n1 → n1 med høy likhet.
	best, sim, err := s.MostSimilarAcceptedFact("t1", []float32{0.99, 0.1, 0}, "kandidat")
	if err != nil || best.ID != "n1" || sim < 0.9 {
		t.Fatalf("ventet n1 med sim > 0.9, fikk %s/%.2f (%v)", best.ID, sim, err)
	}
	// Ekskluderer seg selv: nærmeste til n1s vektor uten n1 er n2.
	best, _, err = s.MostSimilarAcceptedFact("t1", []float32{1, 0, 0}, "n1")
	if err != nil || best.ID != "n2" {
		t.Fatalf("selv-ekskludering feilet, fikk %s (%v)", best.ID, err)
	}
	// Tom tenant → ErrNotFound.
	if _, _, err := s.MostSimilarAcceptedFact("tom", []float32{1}, ""); err != ErrNotFound {
		t.Fatalf("ventet ErrNotFound, fikk %v", err)
	}
}
