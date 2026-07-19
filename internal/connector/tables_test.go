package connector

import (
	"reflect"
	"testing"
)

func TestReferencedTables(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"SELECT * FROM orders", []string{"orders"}},
		{"SELECT a FROM orders o JOIN customers c ON o.cid = c.id", []string{"orders", "customers"}},
		// FROM inne i EXTRACT er ikke en tabell.
		{"SELECT COUNT(*) FROM orders WHERE EXTRACT(YEAR FROM order_creation_date) = 2026", []string{"orders"}},
		{"SELECT SUBSTRING(name FROM 1 FOR 3) FROM customers", []string{"customers"}},
		{"SELECT TRIM(BOTH ' ' FROM name) FROM customers", []string{"customers"}},
	}
	for _, c := range cases {
		got := ReferencedTables(c.query)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ReferencedTables(%q) = %v, vil ha %v", c.query, got, c.want)
		}
	}
}
