package connector

import (
	"context"
	"strings"
	"testing"
)

func TestExpandViews(t *testing.T) {
	got := ExpandViews("SELECT year FROM v_sales_query WHERE year > 2024",
		map[string]string{"v_sales_query": "SELECT * FROM sales WHERE year = 2025"})
	want := "FROM (SELECT * FROM sales WHERE year = 2025) AS v_sales_query"
	if !strings.Contains(got, want) {
		t.Fatalf("utsnittet ble ikke ekspandert: %s", got)
	}
}

// Råtabellen bak et utsnitt skal IKKE være spørrbar direkte — kun via navnet.
func TestViewRawTableRejected(t *testing.T) {
	views := map[string]string{"v_sales_query": "SELECT * FROM sales WHERE year = 2025"}
	_, _, err := SafeQueryViewsN(context.Background(), nil, "postgres",
		"SELECT * FROM sales", nil, views, 10)
	if err == nil || !strings.Contains(err.Error(), "ikke tilgjengelig") {
		t.Fatalf("råtabellen skulle vært avvist, fikk err=%v", err)
	}
}

// Et utsnitt skal også fange RÅTABELL-navnet: FROM v_sales ekspanderes til
// utsnittets SQL når tabellen har et definert utsnitt.
func TestRawTableExpandsToView(t *testing.T) {
	views := map[string]string{
		"v_sales_query": "SELECT * FROM v_sales_raw WHERE year = 2023",
		"v_sales":       "SELECT * FROM v_sales_raw WHERE year = 2023",
	}
	got := ExpandViews("SELECT COUNT(*) FROM v_sales", views)
	if !strings.Contains(got, "WHERE year = 2023) AS v_sales") {
		t.Fatalf("råtabellen ble ikke erstattet av utsnittet: %s", got)
	}
}

// Prod-feilen 2026-07-27: «FROM v_Sales_query s» ble ekspandert til
// «FROM (…) AS v_Sales_query s» — ugyldig SQL, «Incorrect syntax near 's'».
func TestExpandViewsKeepsAlias(t *testing.T) {
	views := map[string]string{"v_sales_query": "SELECT a, b FROM raw WHERE y = 2025"}
	cases := map[string]string{
		// alias uten AS
		"SELECT s.a FROM v_sales_query s WHERE s.a > 1": "FROM (SELECT a, b FROM raw WHERE y = 2025) AS s",
		// alias med AS
		"SELECT s.a FROM v_sales_query AS s": "AS s",
		// uten alias: utsnittsnavnet er aliaset
		"SELECT a FROM v_sales_query WHERE a > 1": "AS v_sales_query WHERE",
		// nøkkelord etter navnet er ikke alias
		"SELECT a FROM v_sales_query ORDER BY a": "AS v_sales_query ORDER",
	}
	for in, want := range cases {
		got := ExpandViews(in, views)
		if !strings.Contains(got, want) {
			t.Errorf("ExpandViews(%q)\n= %q\nmangler %q", in, got, want)
		}
		if strings.Contains(got, "AS v_sales_query s") {
			t.Errorf("alias slukt: %q", got)
		}
	}
}
