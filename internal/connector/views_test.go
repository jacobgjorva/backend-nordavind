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
