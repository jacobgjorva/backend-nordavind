package knowledge

import (
	"sort"
	"strings"
)

// Nærmeste-treff (2026-08-02): når brukeren nevner et internt-utseende navn
// som ikke finnes i treet («svar_scan.py»), skal svaret peke på det
// NÆRMESTE navnet som finnes («scan_svar.py») i stedet for å gjette i løse
// luften. Deterministisk bigram-likhet — transponerte orddeler (svar_scan ↔
// scan_svar) er nettopp det bigrammer fanger og det leksikalsk avstand
// straffer for hardt.

const (
	// hintFloor: målt på det reelle paret svar_scan.py↔scan_svar.py (0.83)
	// og på urelaterte ord (<0.3). Terskelen ligger med god margin mellom.
	hintFloor = 0.55
	// nameTokenMin: korte ord gir falske navnetreff («på» ↔ «py»).
	nameTokenMin = 5
)

// nameHint finner det nærmeste interne navnet til et navne-aktig ord i
// spørsmålet. Tomt svar når ingenting ligner nok — hint uten grunnlag er
// bare en ny måte å gjette på.
func nameHint(names []Candidate, question string) (Candidate, string, bool) {
	var bestName Candidate
	var bestToken string
	var best float64
	for _, tok := range strings.Fields(strings.ToLower(question)) {
		tok = strings.Trim(tok, ".,;:!?«»\"'()")
		if len([]rune(tok)) < nameTokenMin {
			continue
		}
		nameish := strings.ContainsAny(tok, "._-0123456789")
		for _, n := range names {
			name := strings.ToLower(strings.TrimSpace(n.Title))
			if name == "" || name == tok {
				continue // eksakt treff håndteres av selve hentingen
			}
			sim := diceBigrams(tok, name)
			// Vanlige ord («rutinen») skal aldri hintes mot titler de er
			// substreng av — kun ekte navne-tokens, eller nesten-identiske
			// ord med sammenlignbar lengde.
			if !nameish {
				lr := float64(len(tok)) / float64(len(name))
				if lr > 1 {
					lr = 1 / lr
				}
				if sim < 0.8 || lr < 0.7 {
					continue
				}
			}
			if sim > best {
				best, bestName, bestToken = sim, n, tok
			}
		}
	}
	if best < hintFloor {
		return Candidate{}, "", false
	}
	return bestName, bestToken, true
}

// diceBigrams er Dice-koeffisienten over tegn-bigrammer.
func diceBigrams(a, b string) float64 {
	ba, bb := bigrams(a), bigrams(b)
	if len(ba) == 0 || len(bb) == 0 {
		return 0
	}
	common := 0
	for g, n := range ba {
		if m, ok := bb[g]; ok {
			if m < n {
				common += m
			} else {
				common += n
			}
		}
	}
	total := 0
	for _, n := range ba {
		total += n
	}
	for _, n := range bb {
		total += n
	}
	return 2 * float64(common) / float64(total)
}

func bigrams(s string) map[string]int {
	r := []rune(s)
	out := map[string]int{}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])]++
	}
	return out
}

// sortNames gir deterministisk rekkefølge for like scorer i tester.
func sortNames(ns []Candidate) {
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].Title < ns[j].Title })
}
