package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const searxFixture = `{
  "query": "styringsrenten",
  "results": [
    {"title": "Styringsrenten", "url": "https://www.norges-bank.no/styringsrenten", "content": "Styringsrenten er 4,25 prosent.", "engine": "mojeek"},
    {"title": "Renteavgjørelse", "url": "https://e24.no/rente", "content": "Norges Bank holdt renten uendret.", "engine": "brave"},
    {"title": "", "url": "", "content": "rad uten url skal hoppes over"}
  ],
  "unresponsive_engines": [["google", "too many requests"], ["startpage", "timeout"]]
}`

const ddgFixture = `<a rel="nofollow" class="result__a" href="https://ddg.example/side">DDG-treff</a>
<a class="result__snippet">fallback-beskrivelse</a>`

func TestSearxSearchParsesResultsAndDownedEngines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("mangler format=json i %s", r.URL.RawQuery)
		}
		w.Write([]byte(searxFixture))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	var downed []string
	c.Downed = func(e []string) { downed = e }

	results, err := c.Search(context.Background(), "styringsrenten")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("ville hatt 2 treff (tom url droppes), fikk %d", len(results))
	}
	if results[0].Title != "Styringsrenten" || results[0].Description != "Styringsrenten er 4,25 prosent." {
		t.Fatalf("feil parsing: %+v", results[0])
	}
	if len(downed) != 2 || downed[0] != "google" {
		t.Fatalf("unresponsive_engines ikke videreformidlet: %v", downed)
	}
}

// SearXNG nede (403 = glemt json i formats, 500 = krasj) → DDG-fallback.
func TestSearxFallsBackToDDG(t *testing.T) {
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer searx.Close()
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(ddgFixture))
	}))
	defer ddg.Close()

	c := NewClient(searx.URL)
	c.ddgURL = ddg.URL + "/"
	results, err := c.Search(context.Background(), "hva som helst")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "DDG-treff" {
		t.Fatalf("fallback traff ikke DDG: %+v", results)
	}
}

// Tom SEARXNG_URL = ren DDG (lokal dev uten SearXNG kjørende).
func TestEmptySearxURLGoesStraightToDDG(t *testing.T) {
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(ddgFixture))
	}))
	defer ddg.Close()

	c := NewClient("")
	c.ddgURL = ddg.URL + "/"
	results, err := c.Search(context.Background(), "x")
	if err != nil || len(results) != 1 {
		t.Fatalf("DDG-direkteveien feilet: %v %v", results, err)
	}
}
