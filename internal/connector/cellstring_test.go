package connector

import (
	"strings"
	"testing"
	"time"
)

// Store tall ble vist som «1.0260736703026224e+09» i tabellen — ubrukelig i
// et forretningsverktøy, og modellen gjenga det i prosaen.
func TestCellStringNoExponentNotation(t *testing.T) {
	cases := map[any]string{
		float64(1026073670.3026224): "1026073670.3026224",
		float64(1e9):                "1000000000",
		float64(0.5):                "0.5",
		float64(-42.25):             "-42.25",
		float64(0):                  "0",
	}
	for in, want := range cases {
		if got := cellString(in); got != want {
			t.Errorf("cellString(%v) = %q, vil %q", in, got, want)
		}
		if strings.ContainsAny(cellString(in), "eE") {
			t.Errorf("eksponentnotasjon i %q", cellString(in))
		}
	}
}

// De andre typene skal være uendret.
func TestCellStringOtherTypes(t *testing.T) {
	if got := cellString(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	if got := cellString([]byte("tekst")); got != "tekst" {
		t.Errorf("[]byte = %q", got)
	}
	if got := cellString(int64(42)); got != "42" {
		t.Errorf("int64 = %q", got)
	}
	ts := time.Date(2026, 7, 27, 14, 30, 0, 0, time.UTC)
	if got := cellString(ts); got != "2026-07-27 14:30:00" {
		t.Errorf("time = %q", got)
	}
}
