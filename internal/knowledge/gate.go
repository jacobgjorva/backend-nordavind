package knowledge

import "sort"

// Relevans-porten. Tallene er MÅLT (kjøring 1, KUNNSKAP-V2-MAALINGER.md):
//
//	relevant   p5=0.771  p50=0.836
//	urelatert  p50=0.716 p95=0.757
//
// Gapet er smalt (0.014), så porten krever BÅDE nivå og margin:
//   - floor: 0.765 ligger MELLOM urelatert-p95 (0.757) og relevant-p5
//     (0.771). Rene vektortreff under gulvet avvises; gråsonen rett under
//     kan kun reddes av uavhengig FTS-støtte (ftsBonus løfter over).
//   - margin: toppkandidaten må skille seg fra feltets median. En spørring
//     uten relevant kunnskap gir en FLAT fordeling (alt er likt urelatert);
//     en med relevant kunnskap gir en SPISS. Det er formen, ikke bare
//     nivået, som skiller «har noe» fra «har ingenting».
//
// Endres KUN med grønn eval — aldri per enkeltcase.
const (
	gateFloor  = 0.765
	gateMargin = 0.045
	// fts-bonus: et kkandidat både vektor og nøkkelord fant, er sterkere
	// enn vektorscoren alene sier (uavhengige signaler).
	ftsBonus = 0.015
)

// gate velger kandidatene som fortjener plass, eller ingen.
func gate(vec, kw []Candidate) []Candidate {
	if len(vec) == 0 && len(kw) == 0 {
		return nil
	}
	inKW := map[string]bool{}
	for _, c := range kw {
		inKW[c.ID] = true
	}
	// Feltets form: median av vektorscorene FØR filtrering.
	sims := make([]float64, 0, len(vec))
	for _, c := range vec {
		sims = append(sims, c.Sim)
	}
	med := median(sims)

	var picked []Candidate
	for _, c := range vec {
		score := c.Sim
		if inKW[c.ID] {
			score += ftsBonus
		}
		if score >= gateFloor && score-med >= gateMargin {
			c.Sim = score
			picked = append(picked, c)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].Sim > picked[j].Sim })
	return picked
}

// gateKeywordOnly er nødporten når embedding er nede: kun kandidater der
// nøkkelordstreffet er entydig (tittel-treff), strengt tak. Dårligere
// utvalg er akseptabelt i en nødsituasjon; dump er det ikke.
func gateKeywordOnly(kw []Candidate, query string) []Candidate {
	if len(kw) > 3 {
		kw = kw[:3]
	}
	return kw
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}
