package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Source-kolonnen: migrering skal tåle gjenkjøring, insert skal bevare kilden
// og tomt felt skal bli 'chat' (bakoverkompatibelt med gamle kallsteder).
func TestUsageSourceColumn(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrateUsage(); err != nil { // gjenkjøring = ALTER på eksisterende
		t.Fatalf("migrering tålte ikke gjenkjøring: %v", err)
	}
	for _, e := range []UsageEvent{
		{TenantID: "t1", Model: "m", Source: "intent", PromptTokens: 10},
		{TenantID: "t1", Model: "m", PromptTokens: 5}, // tom kilde
	} {
		if err := st.InsertUsage(e); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM usage_events WHERE source IN ('intent','chat')`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ville ha 2 rader med kilde, fikk %d", n)
	}
	_ = os.Remove(filepath.Join(dir, "t.db"))
}
