package api

// Formene under er fra prod-lekkasje nr. 2 (2026-07-30): modellen skrev
// «web\\_search» med markdown-escape, frontenden rendret det som
// «web_search», og vaktene så aldri navnet. Escape-toleranse er derfor en
// del av kontrakten, festet her.

import "testing"

func TestEscapedShapesAreCut(t *testing.T) {
	for _, txt := range []string{
		"i 2026. ---\n\nweb\\_search {\"query\": \"selskaper 2026\"}",
		"prosa. query\\_database {\"query\": \"SELECT 1\"}",
		"prosa. web_search {\"query\": \"x\"}",
		"prosa. web_search {\"query\": \"x\"}",
	} {
		if cut := cutBareToolCall(txt); cut == txt {
			t.Errorf("ikke kuttet: %q", txt[:40])
		}
	}
}

func TestEscapedPrefixHeldBack(t *testing.T) {
	for _, tail := range []string{"tekst web\\_se", "tekst web_se", "tekst query\\_data"} {
		if k := bareToolCallPrefix(tail); k == 0 {
			t.Errorf("halen %q skulle vært holdt igjen", tail)
		}
	}
	if k := bareToolCallPrefix("vanlig tekst uten navn"); k != 0 {
		t.Errorf("vanlig tekst skal ikke holdes igjen, fikk %d", k)
	}
}
