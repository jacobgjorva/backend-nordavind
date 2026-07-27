package store

import (
	"path/filepath"
	"testing"
)

// Summary-kolonnen: gjenkjørbar migrering + set/get + eierskapsvern.
func TestChatSummarySetGet(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Minimal seed uten hjelpere.
	if _, err := st.db.Exec(`INSERT INTO tenants (id, name) VALUES ('t1', 'T')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO users (id, tenant_id, email, role) VALUES ('u1','t1','x@y.no','member')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO chats (id, tenant_id, user_id, title) VALUES ('c1','t1','u1','test')`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatSummary("c1", "u1", "kort sammendrag"); err != nil {
		t.Fatal(err)
	}
	got, err := st.ChatSummary("c1", "u1")
	if err != nil || got != "kort sammendrag" {
		t.Fatalf("ChatSummary = %q, %v", got, err)
	}
	if _, err := st.ChatSummary("c1", "andres-bruker"); err == nil {
		t.Fatal("sammendrag lekket på tvers av brukere")
	}
	if err := st.AppendMessage("c1", "u1", ChatMessage{Role: "user", Content: "hei der"}); err != nil {
		t.Fatal(err)
	}
	n, chars, err := st.ChatSize("c1")
	if err != nil || n != 1 || chars != len("hei der") {
		t.Fatalf("ChatSize = %d, %d, %v", n, chars, err)
	}
}

// Tilstanden som avgjør om websøk må tvinges: hadde forrige svar kilder,
// kan oppfølgingsspørsmål resonnere over dem i stedet for å søke i blinde.
func TestLastAnswerHadSources(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, q := range []string{
		`INSERT INTO tenants (id, name) VALUES ('t1','T')`,
		`INSERT INTO users (id, tenant_id, email, role) VALUES ('u1','t1','x@y.no','member')`,
		`INSERT INTO chats (id, tenant_id, user_id, title) VALUES ('c1','t1','u1','test')`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Ingen meldinger ennå → ingen kilder.
	if had, err := st.LastAnswerHadSources("c1", "u1"); err != nil || had {
		t.Fatalf("tom samtale ga had=%v err=%v", had, err)
	}
	// Svar uten kilder.
	st.AppendMessage("c1", "u1", ChatMessage{Role: "user", Content: "hei"})
	st.AppendMessage("c1", "u1", ChatMessage{Role: "assistant", Content: "hei!"})
	if had, _ := st.LastAnswerHadSources("c1", "u1"); had {
		t.Fatal("svar uten kilder ble regnet som kildebasert")
	}
	// Svar MED kilder.
	st.AppendMessage("c1", "u1", ChatMessage{Role: "user", Content: "årets ord?"})
	st.AppendMessage("c1", "u1", ChatMessage{
		Role: "assistant", Content: "tekoligark",
		Sources: `[{"title":"Språkrådet","url":"https://sprakradet.no"}]`,
	})
	if had, _ := st.LastAnswerHadSources("c1", "u1"); !had {
		t.Fatal("kildebasert svar ble ikke oppdaget")
	}
	// Tom liste teller ikke.
	st.AppendMessage("c1", "u1", ChatMessage{Role: "assistant", Content: "ok", Sources: "[]"})
	if had, _ := st.LastAnswerHadSources("c1", "u1"); had {
		t.Fatal("tom kildeliste ble regnet som kilder")
	}
}
