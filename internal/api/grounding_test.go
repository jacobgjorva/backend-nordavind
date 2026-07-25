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
