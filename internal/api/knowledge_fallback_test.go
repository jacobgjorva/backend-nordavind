package api

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Reserveveien skal finne kunnskap uten et eneste nettverkskall: når
// embeddingen er nede, er nøkkelordindeksen alt vi har — og den ligger
// lokalt i databasen.
func TestKeywordContextFindsNoteWithoutEmbedding(t *testing.T) {
	dir, err := os.MkdirTemp("", "kwtest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const tenant = "t1"
	if _, err := st.SyncFactNote(tenant, "n1", "Ukemøte",
		"Ukentlig ukeplanleggingsmøte med sjefen holdes på mandag.", nil), error(nil); err != nil {
		t.Fatal(err)
	}
	got := s.keywordContext(tenant, "hvilken dag har jeg ukentlig møte med sjefen min?")
	if !strings.Contains(got, "mandag") {
		t.Errorf("fant ikke lappen uten embedding, fikk: %q", got)
	}
}
