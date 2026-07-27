package api

import (
	"strings"
	"testing"
)

// Skjemaet skal advare om STORE tabeller — det er der spørringene timer ut.
// Små tabeller får ingen merknad; da er tallet bare støy i konteksten.
func TestRowHint(t *testing.T) {
	if got := rowHint(4_200_000); got == "" || !strings.Contains(got, "4M") {
		t.Errorf("millioner skal merkes tydelig, fikk %q", got)
	}
	if got := rowHint(250_000); got == "" {
		t.Error("hundretusener skal merkes")
	}
	if got := rowHint(5_000); got != "" {
		t.Errorf("små tabeller skal ikke merkes, fikk %q", got)
	}
	if got := rowHint(0); got != "" {
		t.Errorf("ukjent størrelse skal ikke merkes, fikk %q", got)
	}
}
