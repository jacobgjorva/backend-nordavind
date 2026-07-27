package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const serperFixture = `{
 "answerBox":{"title":"Hailey Bieber","answer":"29 år","link":"https://x.no/a"},
 "knowledgeGraph":{"title":"Hailey Bieber","type":"Modell","description":"Amerikansk modell født 1996.","link":"https://x.no/kg"},
 "organic":[
  {"title":"Treff 1","link":"https://a.no","snippet":"snutt 1"},
  {"title":"Treff 2","link":"https://b.no","snippet":"snutt 2"},
  {"title":"Uten lenke","link":"","snippet":"skal hoppes over"}
 ]}`

func TestSerperParsesAnswerBoxAndOrganic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "hemmelig" {
			t.Errorf("mangler API-nøkkel i header")
		}
		w.Write([]byte(serperFixture))
	}))
	defer srv.Close()

	c := NewClient("", "hemmelig")
	c.serperURL = srv.URL
	got, err := c.Search(context.Background(), "hvor gammel er Hailey Bieber")
	if err != nil {
		t.Fatal(err)
	}
	// Direktesvaret skal komme først — det er ofte hele svaret.
	if got[0].Description != "29 år" {
		t.Fatalf("answerBox kom ikke først: %+v", got[0])
	}
	if got[1].Description != "Amerikansk modell født 1996." {
		t.Fatalf("knowledgeGraph mangler: %+v", got[1])
	}
	if len(got) != 4 { // answerBox + kg + 2 organiske (tom lenke droppet)
		t.Fatalf("ville hatt 4 treff, fikk %d: %+v", len(got), got)
	}
}

// Serper nede → fall videre til SearXNG, ikke stopp søket.
func TestSerperFallsThroughOnError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests) // kvote brukt opp
	}))
	defer dead.Close()
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(searxFixture))
	}))
	defer searx.Close()

	c := NewClient(searx.URL, "hemmelig")
	c.serperURL = dead.URL
	got, err := c.Search(context.Background(), "styringsrenten")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Title != "Styringsrenten" {
		t.Fatalf("falt ikke tilbake til SearXNG: %+v", got)
	}
}

// Uten nøkkel skal Serper aldri kalles.
func TestSerperSkippedWithoutKey(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Write([]byte(serperFixture))
	}))
	defer srv.Close()
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(searxFixture))
	}))
	defer searx.Close()

	c := NewClient(searx.URL, "")
	c.serperURL = srv.URL
	if _, err := c.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("Serper ble kalt uten nøkkel")
	}
}
