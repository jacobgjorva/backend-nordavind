package api

import (
	"fmt"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

func tbl(name string, cols ...string) store.TableConfig {
	t := store.TableConfig{Name: name}
	for _, c := range cols {
		t.ColumnList = append(t.ColumnList, store.ColumnInfo{Name: c, Type: "text"})
	}
	return t
}

// Små baser skal ha ALT med — full oversikt er både billig og best der.
func TestRelevantTablesKeepsSmallSchemaWhole(t *testing.T) {
	small := []store.TableConfig{tbl("orders", "id"), tbl("customers", "id")}
	if relevantTables(small, "hvor mange ordre?") != nil {
		t.Error("liten base skal sendes i sin helhet")
	}
}

// Store baser: bare det spørsmålet peker på får kolonner. Ellers ville 50
// tabeller ligget i hver eneste melding.
func TestRelevantTablesPicksByQuestion(t *testing.T) {
	var big []store.TableConfig
	for i := 0; i < 20; i++ {
		big = append(big, tbl(fmt.Sprintf("tabell_%d", i), "felt"))
	}
	big = append(big, tbl("ordre", "ordrenr", "kunde"), tbl("faktura", "belop"))
	got := relevantTables(big, "hvor mange ordre fikk vi i juni?")
	if got == nil {
		t.Fatal("stor base skulle vært filtrert")
	}
	if !got["ordre"] {
		t.Error("tabellen spørsmålet nevner må ha kolonner med")
	}
	if len(got) > tableDetailLimit+2 {
		t.Errorf("for mange tabeller detaljert: %d", len(got))
	}
}

// Uten fokus (bakgrunnsjobber) skal alt med — vi vet ikke hva som trengs.
func TestRelevantTablesWithoutFocus(t *testing.T) {
	var big []store.TableConfig
	for i := 0; i < 20; i++ {
		big = append(big, tbl(fmt.Sprintf("t%d", i), "felt"))
	}
	if relevantTables(big, "") != nil {
		t.Error("uten fokus kan vi ikke velge — da skal alt med")
	}
}
