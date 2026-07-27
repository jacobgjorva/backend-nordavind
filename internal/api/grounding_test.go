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

func TestGroundingCatchesFabricatedLinks(t *testing.T) {
	sources := []string{`{"columns":["id"],"rows":[["1"]]}`}
	off := groundingOffenders("Fila ligger her: [ordre.xlsx](https://moestuecask.sharepoint.com/x/abc)", sources)
	if len(off) == 0 || !hasNameOffender(off) {
		t.Fatalf("diktet lenke skulle blokkeres hardt, fikk %v", off)
	}
}

// Databaseskjemaet står i query_database-BESKRIVELSEN, ikke i et
// verktøyresultat. Uten den i grunnlaget ble «hvilken data har jeg tilgang
// på?» dømt som dikting selv om modellen siterte skjemaet korrekt.
func TestGroundingBasisIncludesToolDescriptions(t *testing.T) {
	full := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Hvilken data har jeg tilgang på?"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{
				"name": "query_database",
				"description": "Kjør SQL mot bedriftens database.\n\n" +
					"Database \"Kunder\": orders(id, customer_name, total_value), v_Sales(month, sum)",
			}},
		},
	}
	basis := groundingBasis(full, nil)

	answer := "Du har tilgang til tabellen orders og visningen v_Sales i databasen Kunder."
	if off := groundingOffenders(answer, basis); len(off) > 0 {
		t.Fatalf("skjema-navn fra verktøybeskrivelsen ble flagget som dikting: %v", off)
	}
	// Et tabellnavn som IKKE finnes skal fortsatt fanges.
	if off := groundingOffenders("Du har tilgang til Financial_Budget_2099.", basis); len(off) == 0 {
		t.Fatal("oppdiktet tabellnavn slapp gjennom")
	}
}
