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

// Norsk skriver sammensatt: spørsmålet sier «møte», lappen sier
// «Mandagsmøte». Ordbasert søk bommer — delstrengsøket skal redde det.
func TestKeywordContextMatchesCompoundWord(t *testing.T) {
	dir, err := os.MkdirTemp("", "kwtest2")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const tenant = "t1"
	if err := st.SyncFactNote(tenant, "n2", "Ukeplanlegging",
		"Mandagsmøte for å gjennomgå forrige uke og planlegge prioriteringer", nil); err != nil {
		t.Fatal(err)
	}
	got := s.keywordContext(tenant, "hvilken dag har jeg ukentlig møte for ukeplanlegging?")
	if !strings.Contains(strings.ToLower(got), "mandagsmøte") {
		t.Errorf("delstrengsøket fant ikke det sammensatte ordet, fikk: %q", got)
	}
}
