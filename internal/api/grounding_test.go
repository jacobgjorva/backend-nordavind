package api

import "testing"

func TestGroundingCatchesFabrication(t *testing.T) {
	sources := []string{`{"columns":["company","units_sold"],"rows":[["Cask AS","18"],["Ferment AS","470"]]}`}
	off := groundingOffenders("Den nyligste salgsraden er fra Pernod Ricard med 3896 enheter solgt til kunde 22894.", sources)
	if len(off) < 2 {
		t.Fatalf("skulle fanget Pernod Ricard, 3896 og 22894, fikk %v", off)
	}
}

func TestGroundingAcceptsRealValues(t *testing.T) {
	sources := []string{`{"columns":["company","units"],"rows":[["Cask AS","17682"]]}`}
	if off := groundingOffenders("Det er totalt 17 682 ordrer, levert av Cask AS.", sources); len(off) != 0 {
		t.Fatalf("ekte verdier skal passere, fikk avvik: %v", off)
	}
}

func TestGroundingIgnoresSmallCountsAndPlainText(t *testing.T) {
	sources := []string{"### Kilde 1: Matthew McConaughey height — 182 cm (6 ft)"}
	if off := groundingOffenders("Matthew McConaughey er 182 cm høy, ifølge 2 kilder.", sources); len(off) != 0 {
		t.Fatalf("småtall og kildefaste navn skal passere, fikk %v", off)
	}
}

func TestGroundingAcceptsNorwegianGenitive(t *testing.T) {
	sources := []string{"### Kilde 1: Pernod Ricard's annual revenue 2026: $300M and growing"}
	if off := groundingOffenders("Pernod Ricards omsetning er rundt 300 millioner dollar.", sources); len(off) != 0 {
		t.Fatalf("norsk genitiv av kildefast navn skal passere, fikk %v", off)
	}
	sources = []string{"### Kilde 1: Celebrity Net Worth skriver at formuen vokser"}
	if off := groundingOffenders("Celebrity Net Worths anslag viser det samme.", sources); len(off) != 0 {
		t.Fatalf("genitiv-s uten apostrof i kilden skal passere, fikk %v", off)
	}
}

func TestGroundingCatchesFabricatedLinks(t *testing.T) {
	sources := []string{`{"columns":["id"],"rows":[["1"]]}`}
	off := groundingOffenders("Fila ligger her: [ordre.xlsx](https://moestuecask.sharepoint.com/x/abc)", sources)
	if len(off) == 0 || !hasNameOffender(off) {
		t.Fatalf("diktet lenke skulle blokkeres hardt, fikk %v", off)
	}
}
