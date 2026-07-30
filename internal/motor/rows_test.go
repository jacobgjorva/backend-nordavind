package motor

import (
	"strings"
	"testing"
)

// Aggregeringen utløses på et FAKTUM (radantall) og skal bære det koden kan
// si sikkert: størrelse, form, ytterpunkter. Aldri en tolkning.
func TestSummarizeRowsCarriesShapeNotTheList(t *testing.T) {
	cols := []string{"måned", "beløp"}
	var rows [][]string
	for i := 1; i <= 40; i++ {
		rows = append(rows, []string{string(rune('0' + i%10)), "100"})
	}
	got := SummarizeRows(cols, rows)

	if !strings.Contains(got, "40 rader") {
		t.Errorf("antallet mangler: %q", got)
	}
	if !strings.Contains(got, "måned, beløp") {
		t.Errorf("kolonnene mangler: %q", got)
	}
	if !strings.Contains(got, "Første rad:") || !strings.Contains(got, "Siste rad:") {
		t.Errorf("ytterpunktene mangler: %q", got)
	}
	if !strings.Contains(got, "show_table") {
		t.Error("brukeren skal fortsatt kunne få hele tabellen")
	}
	if len(got) > 500 {
		t.Errorf("sammendraget er %d tegn — da er det ikke lenger et sammendrag", len(got))
	}
}

// Tom tabell skal ikke indeksere ut av rekkevidde.
func TestSummarizeRowsHandlesEmpty(t *testing.T) {
	got := SummarizeRows([]string{"a"}, nil)
	if !strings.Contains(got, "0 rader") {
		t.Errorf("uventet: %q", got)
	}
}
