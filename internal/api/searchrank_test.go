package api

import (
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

func TestDemoteSocial(t *testing.T) {
	in := []search.Result{
		{URL: "https://www.youtube.com/watch?v=x", Title: "video"},
		{URL: "https://no.wikipedia.org/wiki/Aurora_Aksnes", Title: "wiki"},
		{URL: "https://www.reddit.com/r/x", Title: "reddit"},
		{URL: "https://snl.no/Aurora", Title: "snl"},
	}
	out := demoteSocial(in)
	// Faktakildene skal komme først, i original rekkefølge.
	if out[0].Title != "wiki" || out[1].Title != "snl" {
		t.Fatalf("faktakilder havnet ikke først: %v", []string{out[0].Title, out[1].Title})
	}
	// Sosiale medier bakerst, men beholdt (ingen fjernes).
	if out[2].Title != "video" && out[2].Title != "reddit" {
		t.Fatalf("sosiale medier ikke bakerst: %+v", out)
	}
	if len(out) != 4 {
		t.Fatalf("resultater ble fjernet: %d", len(out))
	}
}

func TestIsSocialPrecision(t *testing.T) {
	social := []string{"https://youtube.com/x", "https://www.reddit.com/r/y",
		"https://m.youtube.com/z", "https://x.com/user"}
	notSocial := []string{"https://no.wikipedia.org/wiki/X", "https://snl.no/x",
		"https://norges-bank.no", "https://firma.no/reddit-analyse", // «reddit» i sti, ikke domene
		"https://sprakradet.no"}
	for _, u := range social {
		if !isSocial(u) {
			t.Errorf("isSocial(%q) = false, skal være sosial", u)
		}
	}
	for _, u := range notSocial {
		if isSocial(u) {
			t.Errorf("isSocial(%q) = true, skal IKKE være sosial", u)
		}
	}
}
