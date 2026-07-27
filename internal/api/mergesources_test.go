package api

import (
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

func TestMergeSources(t *testing.T) {
	motor := []search.Result{{URL: "https://a.no", Title: "motor1"}, {URL: "https://b.no", Title: "motor2"}}
	wiki := []search.Result{{URL: "https://b.no", Title: "duplikat"}, {URL: "https://no.wikipedia.org/wiki/X", Title: "wiki"}}

	got := mergeSources(motor, wiki)
	if len(got) != 3 {
		t.Fatalf("ville hatt 3 (duplikat fjernet), fikk %d: %+v", len(got), got)
	}
	if got[0].Title != "motor1" || got[1].Title != "motor2" {
		t.Fatal("motortreff mistet rekkefølgen sin")
	}
	if got[2].Title != "wiki" {
		t.Fatalf("wikipedia skal komme etter motortreff, fikk %q", got[2].Title)
	}
}

// Hovedgevinsten: er motorene blokkert, bærer Wikipedia turen alene.
func TestMergeSourcesCarriesWhenEnginesBlocked(t *testing.T) {
	got := mergeSources(nil, []search.Result{{URL: "https://no.wikipedia.org/wiki/Penger", Title: "Penger"}})
	if len(got) != 1 || got[0].Title != "Penger" {
		t.Fatalf("wikipedia bar ikke turen alene: %+v", got)
	}
}
