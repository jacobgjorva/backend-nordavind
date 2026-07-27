package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const wikiFixture = `{"query":{"search":[
 {"title":"Erna Solberg","snippet":"<span class=\"searchmatch\">Erna</span> Solberg er en norsk politiker født 24. februar 1961."},
 {"title":"Pinterest","snippet":"Pinterest er et nettsamfunn grunnlagt av Ben Silbermann."},
 {"title":"Penger","snippet":"Penger er et allment akseptert betalingsmiddel."},
 {"title":"Fjerde treff","snippet":"skal kuttes"}
]}}`

func TestWikiSearchParsesAndStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "query" || r.URL.Query().Get("srsearch") == "" {
			t.Errorf("uventet spørring: %s", r.URL.RawQuery)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("mangler User-Agent — Wikimedia svarer 429 uten")
		}
		w.Write([]byte(wikiFixture))
	}))
	defer srv.Close()

	c := NewClient("", "")
	c.wikiNO = srv.URL + "/w/api.php"
	got := c.WikiSearch(context.Background(), "Erna Solberg")

	if len(got) != wikiMaxResults {
		t.Fatalf("ville hatt %d treff, fikk %d", wikiMaxResults, len(got))
	}
	if strings.Contains(got[0].Description, "<span") {
		t.Fatalf("HTML ble ikke strippet: %q", got[0].Description)
	}
	if !strings.Contains(got[0].Description, "24. februar 1961") {
		t.Fatalf("faktum forsvant i strippingen: %q", got[0].Description)
	}
	if !strings.HasSuffix(got[0].URL, "/wiki/Erna_Solberg") {
		t.Fatalf("feil artikkel-URL: %q", got[0].URL)
	}
}

// Tomt på bokmål → prøv engelsk (tekniske temaer finnes ofte kun der).
func TestWikiSearchFallsBackToEnglish(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"query":{"search":[]}}`))
	}))
	defer empty.Close()
	en := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"query":{"search":[{"title":"Data labeling","snippet":"Labeling for machine learning."}]}}`))
	}))
	defer en.Close()

	c := NewClient("", "")
	c.wikiNO = empty.URL + "/w/api.php"
	c.wikiEN = en.URL + "/w/api.php"
	got := c.WikiSearch(context.Background(), "data labeling")
	if len(got) != 1 || got[0].Title != "Data labeling" {
		t.Fatalf("engelsk fallback traff ikke: %+v", got)
	}
}

// Fail-soft: HTTP-feil skal aldri stoppe søket, bare gi tom liste.
func TestWikiSearchFailsSoft(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer down.Close()
	c := NewClient("", "")
	c.wikiNO, c.wikiEN = down.URL+"/w/api.php", down.URL+"/w/api.php"
	if got := c.WikiSearch(context.Background(), "hva som helst"); got != nil {
		t.Fatalf("ville hatt tom liste ved 429, fikk %+v", got)
	}
}
