package knowledge

import (
	"strings"
	"testing"
)

// Fordelingene under er syntetiske kopier av kalibreringen (kjøring 1):
// urelatert klynger 0.66-0.757, relevant ligger 0.77+. Testene fester
// portens FORM-krav: flat fordeling = ingenting, spiss = toppen.

func cands(sims ...float64) []Candidate {
	out := make([]Candidate, len(sims))
	for i, s := range sims {
		out[i] = Candidate{ID: string(rune('a' + i)), Text: "x", Sim: s}
	}
	return out
}

// «takk for hjelpen med lagertallene»-formen: alt er middels likt, ingenting
// relevant. Porten skal tie — dette var v1s 2 700-tegns feil.
func TestGateStaysQuietOnFlatField(t *testing.T) {
	flat := cands(0.757, 0.744, 0.741, 0.733, 0.728, 0.721, 0.716, 0.705)
	if got := gate(flat, nil); len(got) != 0 {
		t.Fatalf("flat fordeling skal gi tomt utvalg, fikk %d", len(got))
	}
}

// Ekte treff: én kandidat skiller seg klart fra feltet.
func TestGatePicksTheSpike(t *testing.T) {
	spiky := cands(0.836, 0.744, 0.731, 0.722, 0.716, 0.709)
	got := gate(spiky, nil)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("forventet kun toppkandidaten, fikk %+v", got)
	}
}

// Gråsonen: 0.760 alene er under gulvet — men med uavhengig FTS-støtte
// (+0.015) og god margin skal den med. To signaler slår ett.
func TestGateLetsFTSSupportRescueGrayZone(t *testing.T) {
	vec := cands(0.760, 0.716, 0.709, 0.701, 0.695)
	if got := gate(vec, nil); len(got) != 0 {
		t.Fatalf("uten FTS-støtte skal gråsonen avvises, fikk %d", len(got))
	}
	kw := []Candidate{{ID: "a"}}
	got := gate(vec, kw)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("med FTS-støtte skal gråsonen med, fikk %+v", got)
	}
}

// Tomt inn = tomt ut, aldri panikk.
func TestGateHandlesEmpty(t *testing.T) {
	if got := gate(nil, nil); got != nil {
		t.Fatalf("tomt felt skal gi nil, fikk %+v", got)
	}
}

// Flere ekte treff beholdes alle, sortert sterkest først.
func TestGateKeepsAllRealHitsSorted(t *testing.T) {
	vec := cands(0.812, 0.836, 0.729, 0.716, 0.708, 0.699)
	got := gate(vec, nil)
	if len(got) != 2 || got[0].Sim < got[1].Sim {
		t.Fatalf("forventet to treff sortert synkende, fikk %+v", got)
	}
}

// Prod-buggen 2026-08-02: én stor bit (2 300 tegn) røk på budsjettet og
// turen fikk «treff» med null linjer. Store biter trunkeres, droppes aldri.
func TestRenderTruncatesOversizedTopCandidate(t *testing.T) {
	big := Candidate{ID: "a", Text: strings.Repeat("kunnskap ", 300)} // ~2700 tegn
	b := render([]Candidate{big})
	if b.Lines != 1 || b.Text == "" {
		t.Fatalf("stor bit skulle vært trunkert inn, fikk %d linjer", b.Lines)
	}
	if len(b.Text) > charBudget+100 {
		t.Fatalf("budsjettet sprengt: %d tegn", len(b.Text))
	}
}
