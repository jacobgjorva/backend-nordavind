package api

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

// Hver terskel låses med BÅDE positivt og negativt tilfelle. Det negative er
// det viktigste: en observasjon som kommer for ofte er støy, ikke innsikt.

func TestObserveConcentration(t *testing.T) {
	cols := []string{"customer_name", "total"}
	skew := [][]string{{"A", "410"}, {"B", "150"}, {"C", "140"}, {"D", "150"}, {"E", "150"}}
	in, ok := observe(cols, skew, "SELECT ... ORDER BY total DESC", "hvem er størst?", 100, testNow)
	if !ok || in.kind != insConcentration {
		t.Fatalf("skjev fordeling ble ikke oppdaget: %+v", in)
	}
	if in.val != "41 %" {
		t.Errorf("andel ble %q, ventet 41 %%", in.val)
	}
}

// «Ganske jevnt fordelt» er ikke en observasjon, det er fyll.
func TestObserveEvenDistributionIsSilent(t *testing.T) {
	cols := []string{"navn", "total"}
	even := [][]string{{"A", "100"}, {"B", "100"}, {"C", "100"}, {"D", "100"}, {"E", "100"}}
	if in, ok := observe(cols, even, "SELECT ... ORDER BY total DESC", "hvem er størst?", 100, testNow); ok {
		t.Errorf("jevn fordeling skal ikke gi observasjon, fikk %+v", in)
	}
}

func TestObservePartialMonthOnGrowthQuery(t *testing.T) {
	cols := []string{"maned", "omsetning"}
	rows := [][]string{{"2026-05-01", "100"}, {"2026-06-01", "120"}, {"2026-07-01", "60"}}
	in, ok := observe(cols, rows, "SELECT ... GROUP BY maned", "vokser vi?", 100, testNow)
	if !ok || in.kind != insPartialPeriod {
		t.Fatalf("uferdig måned ble ikke oppdaget: %+v", in)
	}
	// Sent i måneden er delvisheten uvesentlig, og påminnelsen bare masete.
	late := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	if in, ok := observe(cols, rows, "SELECT ... GROUP BY maned", "vokser vi?", 100, late); ok &&
		in.kind == insPartialPeriod {
		t.Errorf("dag 29 skal ikke utløse uferdig-måned, fikk %+v", in)
	}
}

func TestObserveRowCapHonest(t *testing.T) {
	cols := []string{"navn"}
	full := make([][]string, 100)
	for i := range full {
		full[i] = []string{fmt.Sprintf("Kunde %d", i)} // unike: ellers vinner duplikat-detektoren, med rette
	}
	in, ok := observe(cols, full, "SELECT navn FROM kunder", "vis kundene", 100, testNow)
	if !ok || in.kind != insRowCap {
		t.Fatalf("radgrensen ble ikke meldt: %+v", in)
	}
	// Ba brukeren selv om 100, er grensen ingen overraskelse.
	if in, ok := observe(cols, full, "SELECT navn FROM kunder", "vis de 100 største", 100, testNow); ok &&
		in.kind == insRowCap {
		t.Errorf("brukeren ba selv om 100, skal ikke meldes: %+v", in)
	}
	if _, ok := observe(cols, full[:99], "SELECT navn FROM kunder", "vis kundene", 100, testNow); ok {
		t.Error("99 rader traff ikke taket og skal ikke meldes")
	}
}

func TestObserveStaleRespectsDeliberateFilter(t *testing.T) {
	cols := []string{"dato", "sum"}
	rows := [][]string{{"2024-03-14", "10"}, {"2024-03-15", "20"}}
	in, ok := observe(cols, rows, "SELECT dato, sum FROM ordre", "hva er tallene?", 100, testNow)
	if !ok || in.kind != insStale {
		t.Fatalf("gamle data ble ikke meldt: %+v", in)
	}
	if !strings.Contains(in.val, "mars") {
		t.Errorf("datoen skal være på norsk, fikk %q", in.val)
	}
	// Spurte brukeren bevisst om 2024, er «dataene stopper i 2024» en fornærmelse.
	if in, ok := observe(cols, rows, "SELECT ... WHERE year = 2024", "hva skjedde i 2024?", 100, testNow); ok &&
		in.kind == insStale {
		t.Errorf("bevisst datofilter skal undertrykke: %+v", in)
	}
}

func TestObserveConstantColumnOnlyWhenUsed(t *testing.T) {
	cols := []string{"customer_name", "credit_limit"}
	rows := [][]string{{"A", "0"}, {"B", "0"}, {"C", "0"}, {"D", "0"}}
	// Det målte tilfellet: sortert på en kolonne som er 0 for alle.
	in, ok := observe(cols, rows, "SELECT ... ORDER BY credit_limit ASC", "hvem har lavest kredittverdighet?", 100, testNow)
	if !ok || in.kind != insConstantCol || in.col != "credit_limit" {
		t.Fatalf("ubrukt kolonne ble ikke meldt: %+v", in)
	}
	// Ingen spurte om kolonnen, ingen sorterte på den: da er den bare støy.
	if in, ok := observe(cols, rows, "SELECT customer_name, credit_limit FROM customers", "list kundene", 100, testNow); ok &&
		in.kind == insConstantCol {
		t.Errorf("ubrukt kolonne ingen brydde seg om skal være stille: %+v", in)
	}
}

// Feil-før-interessant: står valget mellom «svaret ditt er misvisende» og
// «dette er verdt å vite», vinner det første.
func TestObservePriorityIsSeverity(t *testing.T) {
	cols := []string{"navn", "credit_limit"}
	rows := [][]string{{"A", "0"}, {"B", "0"}, {"C", "0"}, {"D", "0"}, {"E", "0"}}
	in, ok := observe(cols, rows, "SELECT ... ORDER BY credit_limit DESC", "hvem har høyest kredittgrense?", 100, testNow)
	if !ok || in.kind != insConstantCol {
		t.Fatalf("konstant kolonne skal slå konsentrasjon, fikk %+v", in)
	}
}

func TestObserveDuplicatesCatchesJoinFanout(t *testing.T) {
	cols := []string{"kunde", "ordre_id"}
	rows := [][]string{{"A", "1"}, {"A", "2"}, {"A", "3"}, {"B", "4"}, {"B", "5"}, {"B", "6"}}
	in, ok := observe(cols, rows, "SELECT kunde, ordre_id FROM ordre JOIN kunder", "vis kundene", 100, testNow)
	if !ok || in.kind != insDuplicates {
		t.Fatalf("join-fanout ble ikke oppdaget: %+v", in)
	}
	if in.other != 2 || in.n != 6 {
		t.Errorf("telling feil: %d unike av %d rader", in.other, in.n)
	}
}

func TestProfileColumnsTypesRealResult(t *testing.T) {
	cols := []string{"customer_id", "customer_name", "dato", "omsetning"}
	rows := [][]string{
		{"1", "Racamaca AB", "2026-07-01", "1 473 032,49"},
		{"2", "Brüdog Bar", "2026-07-02", "980 000,00"},
	}
	ps := profileColumns(cols, rows)
	if !ps[0].idLike {
		t.Error("customer_id skal gjenkjennes som id")
	}
	if !ps[2].dateish {
		t.Error("dato-kolonnen ble ikke gjenkjent som dato")
	}
	if !ps[3].numeric || len(ps[3].nums) != 2 {
		t.Errorf("norsk tallformat ble ikke parset: %+v", ps[3].nums)
	}
	m, ok := measureColumn(ps)
	if !ok || m.name != "omsetning" {
		t.Errorf("måltallet ble %q, ventet omsetning (ikke id-kolonnen)", m.name)
	}
}

// Alle portene som holder støyen ute.
func TestDeliverInsightSuppressions(t *testing.T) {
	in := insight{kind: insConcentration, col: "total", val: "41 %", n: 5}

	cases := []struct {
		name   string
		on     bool
		ins    insight
		answer string
		limit  int
		want   bool // skal noe sendes?
	}{
		{"normal", true, in, "De ti største står for 12 prosent.", 300, true},
		{"utenfor vanlig chat", false, in, "De ti største står for 12 prosent.", 300, false},
		{"ingen observasjon", true, insight{}, "Svar.", 300, false},
		{"svaret er et spørsmål", true, in, "Hvilken periode mener du?", 300, false},
		{"modellen nevnte tallet", true, in, "Den øverste tar 41 % av totalen.", 300, false},
		{"modellen nevnte kolonnen", true, in, "Sortert på total.", 300, false},
		{"over plassbudsjettet", true, in, strings.Repeat("x", 290), 300, false},
	}
	for _, c := range cases {
		var sent []string
		n := newNarrator(func(line string) { sent = append(sent, line) }, c.on, nil)
		n.deliverInsight(n.emit, c.ins, c.answer, c.limit, true)
		if got := len(sent) > 0; got != c.want {
			t.Errorf("%s: sendte=%v, ventet=%v (%v)", c.name, got, c.want, sent)
		}
	}
}

// Samme setningskontrakt som narrasjonsregisteret, pluss lengdetaket.
func TestInsightSentencesAreWellFormed(t *testing.T) {
	samples := map[insightKind]insight{
		insConstantCol:   {col: "credit_limit", val: "0", n: 100},
		insDuplicates:    {col: "kunde", n: 40, other: 12},
		insPartialPeriod: {col: "maned", val: "juli", n: 12},
		insRowCap:        {n: 100},
		insStale:         {col: "dato", val: "14. mars"},
		insConcentration: {col: "total", val: "41 %", n: 10},
		insNulls:         {col: "sluttdato", n: 40, other: 14},
	}
	for kind, sample := range samples {
		sample.kind = kind
		for turn := 0; turn < 12; turn++ {
			n := newNarrator(func(string) {}, true, nil)
			n.turn = turn
			s := insightSentence(n, sample)
			if strings.TrimSpace(s) == "" {
				t.Errorf("kind %d turn %d: tom setning", kind, turn)
				continue
			}
			if !strings.HasSuffix(s, ".") {
				t.Errorf("kind %d: mangler punktum: %q", kind, s)
			}
			r := []rune(s)
			if r[0] != []rune(strings.ToUpper(string(r[0])))[0] {
				t.Errorf("kind %d: liten forbokstav: %q", kind, s)
			}
			if len(r) > maxInsightChars {
				t.Errorf("kind %d: %d tegn, taket er %d: %q", kind, len(r), maxInsightChars, s)
			}
			// Observerende, aldri rådgivende.
			for _, banned := range []string{"du bør", "kanskje du", "vil du at jeg"} {
				if strings.Contains(strings.ToLower(s), banned) {
					t.Errorf("kind %d: rådgivende formulering %q i %q", kind, banned, s)
				}
			}
		}
	}
}

// Den eneste antakelsen som lever i frontend-repoet: setningen blir del av
// assistentmeldingen, så tallet er dekket når brukeren følger opp på det.
func TestInsightIsGroundedNextTurn(t *testing.T) {
	answer := "De ti største kundene står for 12,15 % av alle ordrene."
	sentence := "Fordelingen er skjev: den øverste tar 41 % av totalen alene."
	full := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "Hvor stor andel kommer fra de ti største?"},
		map[string]any{"role": "assistant", "content": answer + " " + sentence},
	}}
	basis := groundingBasis(full, nil)
	if off := groundingOffenders("Ja, 41 % fra den øverste stemmer.", basis); len(off) > 0 {
		t.Errorf("innsiktstallet var ikke dekket i neste tur: %v", off)
	}
}

// Kolonnen ble filtrert på, men brukeren spurte på norsk mens kolonnen har
// engelsk navn. Filteret er signalet, ikke ordlikheten.
func TestObserveConstantColumnFromWhereClause(t *testing.T) {
	cols := []string{"customer_name", "credit_limit"}
	rows := [][]string{{"A", "0"}, {"B", "0"}, {"C", "0"}, {"D", "0"}}
	in, ok := observe(cols, rows, "SELECT customer_name, credit_limit FROM customers WHERE credit_limit = 0 LIMIT 10",
		"hvilke kunder har lavest kredittverdighet?", 100, testNow)
	if !ok || in.kind != insConstantCol {
		t.Fatalf("WHERE-filter skal telle som i bruk: %+v", in)
	}
}

// En observasjon skal alltid tilhøre resultatet svaret bygger på. observe må
// derfor si tydelig fra når den ikke har noe — kalleren nullstiller da den
// forrige, i stedet for å la den henge igjen og bli limt på et svar om noe
// annet. Dette er kontrakten agent.go lener seg på.
func TestObserveReportsNothingClearly(t *testing.T) {
	cols := []string{"navn", "total"}
	plain := [][]string{{"A", "100"}, {"B", "99"}, {"C", "98"}}
	in, ok := observe(cols, plain, "SELECT navn, total FROM x", "hva er tallene?", 100, testNow)
	if ok {
		t.Fatalf("intet å observere, men fikk %+v", in)
	}
	if in.kind != insNone {
		t.Errorf("tom observasjon skal ha insNone, fikk %d", in.kind)
	}
}

// Radgrensen er bare relevant for en liste brukeren faktisk så.
func TestDeliverInsightRowCapNeedsList(t *testing.T) {
	in := insight{kind: insRowCap, n: 100}
	for _, listShown := range []bool{true, false} {
		var sent []string
		n := newNarrator(func(line string) { sent = append(sent, line) }, true, nil)
		n.deliverInsight(n.emit, in, "Margindata finnes ikke.", 300, listShown)
		if got := len(sent) > 0; got != listShown {
			t.Errorf("listShown=%v: sendte=%v", listShown, got)
		}
	}
}
