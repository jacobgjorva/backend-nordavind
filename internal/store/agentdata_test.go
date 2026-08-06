package store

import (
	"strings"
	"testing"
)

func lagerTable() DataTable {
	return DataTable{
		Name: "varer",
		Columns: []DataColumn{
			{Name: "navn", Type: "text"},
			{Name: "antall", Type: "number"},
			{Name: "sist_talt", Type: "date"},
		},
		Key: "navn",
	}
}

func TestValidateDataSchemaAcceptsSane(t *testing.T) {
	if p := ValidateDataSchema([]DataTable{lagerTable()}); len(p) != 0 {
		t.Fatalf("gyldig skjema skal passere, fikk %v", p)
	}
}

func TestValidateDataSchemaRejectsBadNamesTypesAndKey(t *testing.T) {
	bad := []DataTable{
		{Name: "Varer!", Columns: []DataColumn{{Name: "a", Type: "text"}}},
		{Name: "ok", Columns: []DataColumn{{Name: "Robert'); DROP", Type: "text"}}},
		{Name: "ok2", Columns: []DataColumn{{Name: "a", Type: "jsonb"}}},
		{Name: "ok3", Columns: []DataColumn{{Name: "a", Type: "text"}}, Key: "finnes_ikke"},
		{Name: "select", Columns: []DataColumn{{Name: "a", Type: "text"}}},
	}
	p := ValidateDataSchema(bad)
	if len(p) != 5 {
		t.Fatalf("ventet 5 problemer, fikk %d: %v", len(p), p)
	}
}

func TestCreateDataTableSQL(t *testing.T) {
	got := createDataTableSQL("agentdata_abc", lagerTable())
	want := `CREATE TABLE IF NOT EXISTS "agentdata_abc"."varer" ("navn" TEXT PRIMARY KEY, "antall" DOUBLE PRECISION, "sist_talt" DATE, "oppdatert" TIMESTAMPTZ NOT NULL DEFAULT now())`
	if got != want {
		t.Fatalf("uventet DDL:\n%s\nville hatt:\n%s", got, want)
	}
}

func TestUpsertDataSQLDeterministicAndKeyed(t *testing.T) {
	q, args, err := upsertDataSQL("s", lagerTable(), map[string]string{"antall": "12", "navn": "gin 0.5l"})
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "s"."varer" ("antall", "navn") VALUES (?, ?) ON CONFLICT ("navn") DO UPDATE SET "antall" = EXCLUDED."antall", "oppdatert" = now()`
	if q != want {
		t.Fatalf("uventet SQL:\n%s", q)
	}
	if len(args) != 2 || args[0] != "12" || args[1] != "gin 0.5l" {
		t.Fatalf("uventede args: %v", args)
	}
}

func TestUpsertDataSQLRejectsUnknownColumnAndMissingKey(t *testing.T) {
	if _, _, err := upsertDataSQL("s", lagerTable(), map[string]string{"pris": "9"}); err == nil {
		t.Fatal("ukjent kolonne skulle avvises")
	}
	if _, _, err := upsertDataSQL("s", lagerTable(), map[string]string{"antall": "9"}); err == nil {
		t.Fatal("manglende nøkkel skulle avvises")
	}
}

func TestDeleteDataSQLRequiresFilter(t *testing.T) {
	if _, _, err := deleteDataSQL("s", lagerTable(), nil); err == nil {
		t.Fatal("sletting uten filter skulle avvises")
	}
	q, args, err := deleteDataSQL("s", lagerTable(), map[string]string{"navn": "gin 0.5l"})
	if err != nil || !strings.Contains(q, `WHERE "navn" = ?`) || len(args) != 1 {
		t.Fatalf("uventet delete: %s %v %v", q, args, err)
	}
}

func TestAgentDataSchemaSanitizesID(t *testing.T) {
	if got := agentDataSchema("AG-12.\"x\"; DROP--"); got != "agentdata_ag12xdrop" {
		t.Fatalf("uventet schemanavn: %s", got)
	}
}
