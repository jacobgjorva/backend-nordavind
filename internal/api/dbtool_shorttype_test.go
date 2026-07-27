package api

import "testing"

// Typenavnene står i HVER melding som har databasen med. «timestamp with
// time zone» × 75 kolonner er ren kontekst-kostnad uten informasjonsgevinst.
func TestShortType(t *testing.T) {
	cases := map[string]string{
		"timestamp with time zone": "ts",
		"character varying(255)":   "text",
		"bigint":                   "int",
		"numeric(12,2)":            "num",
		"boolean":                  "bool",
		"date":                     "date",
		"":                         "",
		"geography":                "geography", // ukjent: la stå
	}
	for in, want := range cases {
		if got := shortType(in); got != want {
			t.Errorf("shortType(%q) = %q, ville %q", in, got, want)
		}
	}
}
