package api

import (
	"strings"
	"testing"
)

func TestLevenshteinFindsMisspelling(t *testing.T) {
	// Det målte tilfellet: brukeren skrev «Racamca AB», basen har «Racamaca AB».
	got := rankByCloseness("Racamca AB", []string{
		"Racamaca AB", "Racamaca Söder AB", "Helt Annet Firma AB", "Rabbit AB",
	}, 3)
	if len(got) == 0 || got[0] != "Racamaca AB" {
		t.Fatalf("nærmeste treff ble %v, ventet Racamaca AB først", got)
	}
	for _, g := range got {
		if g == "Helt Annet Firma AB" {
			t.Errorf("vilt forskjellig kandidat slapp gjennom: %q", g)
		}
	}
}

func TestRankByClosenessRejectsNonsense(t *testing.T) {
	if got := rankByCloseness("Racamca", []string{"Zebra", "Qwerty"}, 3); len(got) != 0 {
		t.Errorf("dårlige forslag er verre enn ingen, fikk %v", got)
	}
}

func TestConstantColumnCatchesUnusedField(t *testing.T) {
	// Det målte tilfellet: credit_limit er 0 for alle, så «lavest» er tomt snakk.
	cols := []string{"customer_name", "credit_limit"}
	rows := [][]string{{"A", "0"}, {"B", "0"}, {"C", "0"}, {"D", "0"}}
	col, val := constantColumn(cols, rows)
	if col != "credit_limit" || val != "0" {
		t.Fatalf("fikk (%q, %q), ventet (credit_limit, 0)", col, val)
	}
}

func TestConstantColumnIgnoresRealVariation(t *testing.T) {
	cols := []string{"name", "total"}
	rows := [][]string{{"A", "10"}, {"B", "20"}, {"C", "30"}}
	if col, _ := constantColumn(cols, rows); col != "" {
		t.Errorf("varierende kolonne ble feilmeldt som konstant: %q", col)
	}
}

// En kolonne som er lik overalt, men med en meningsfull verdi, er ikke en feil.
func TestConstantColumnIgnoresMeaningfulSameness(t *testing.T) {
	cols := []string{"name", "country"}
	rows := [][]string{{"A", "Sverige"}, {"B", "Sverige"}, {"C", "Sverige"}}
	if col, _ := constantColumn(cols, rows); col != "" {
		t.Errorf("felles land skal ikke meldes som ubrukt kolonne: %q", col)
	}
}

func TestExplainAttemptsNoAccessSaysWhatWeHave(t *testing.T) {
	got := explainAttempts(
		[]dbAttempt{{outcome: dbNoAccess}},
		[]string{"customers", "orders", "feedback"},
	)
	for _, want := range []string{"customers", "orders", "feedback"} {
		if !strings.Contains(got, want) {
			t.Errorf("redegjørelsen nevner ikke %q: %s", want, got)
		}
	}
	if strings.Contains(got, "svarte ikke i tide") {
		t.Errorf("manglende tilgang skal ikke meldes som timeout: %s", got)
	}
}

func TestExplainAttemptsTimeoutIsHonest(t *testing.T) {
	got := explainAttempts([]dbAttempt{{outcome: dbFailed}}, []string{"orders"})
	if !strings.Contains(got, "ikke i tide") {
		t.Errorf("ekte timeout skal sies som timeout: %s", got)
	}
}

func TestExplainAttemptsEmptyAsksForWhatItNeeds(t *testing.T) {
	got := explainAttempts(
		[]dbAttempt{{outcome: dbEmpty}, {outcome: dbEmpty}},
		[]string{"orders"},
	)
	if !strings.Contains(got, "2 oppslag") {
		t.Errorf("skal si hvor mange forsøk som ble gjort: %s", got)
	}
	if !strings.Contains(strings.ToLower(got), "vet du") {
		t.Errorf("skal spørre brukeren om det den trenger: %s", got)
	}
}

func TestHumanList(t *testing.T) {
	for in, want := range map[string]string{
		"a":     "a",
		"a|b":   "a og b",
		"a|b|c": "a, b og c",
	} {
		if got := humanList(strings.Split(in, "|")); got != want {
			t.Errorf("humanList(%q) = %q, ventet %q", in, got, want)
		}
	}
}

func TestSafeIdentBlocksInjection(t *testing.T) {
	for _, bad := range []string{"name; DROP TABLE x", "a b", "col'", "", strings.Repeat("x", 65)} {
		if safeIdent(bad) {
			t.Errorf("slapp gjennom farlig identifikator: %q", bad)
		}
	}
	if !safeIdent("customer_name") {
		t.Error("vanlig kolonnenavn ble avvist")
	}
}

func TestAnyOKAndLastAttempt(t *testing.T) {
	as := []dbAttempt{{outcome: dbEmpty}, {outcome: dbOK}, {outcome: dbNoAccess}}
	if !anyOK(as) {
		t.Error("anyOK fant ikke det vellykkede forsøket")
	}
	if lastAttempt(as).outcome != dbNoAccess {
		t.Error("lastAttempt ga feil forsøk")
	}
	if lastAttempt(nil).outcome != dbOK {
		t.Error("tom liste skal gi nullverdien")
	}
}

func TestZeroAggregateDetectsMiss(t *testing.T) {
	if !zeroAggregate([]string{"total"}, [][]string{{"0"}}) {
		t.Error("COUNT som ga 0 skal regnes som bom, ikke som svar")
	}
	if !zeroAggregate([]string{"sum", "n"}, [][]string{{"0.00", "0"}}) {
		t.Error("aggregat med bare nuller skal regnes som bom")
	}
	if zeroAggregate([]string{"total"}, [][]string{{"1473032.49"}}) {
		t.Error("ekte tall skal ikke regnes som bom")
	}
	if zeroAggregate([]string{"a"}, [][]string{{"0"}, {"0"}}) {
		t.Error("flere rader er ikke et enkelt-aggregat")
	}
}

// Et aggregat uten treff skal klassifiseres som TOMT, ikke som et vellykket
// oppslag. Ellers tror dbSucceeded at vi fant noe, kvalitetsporten står ned,
// og fraværet kan formuleres som et funn («har kjøpt for 0 kroner»).
func TestZeroAggregateCountsAsEmptyOutcome(t *testing.T) {
	cols := []string{"total"}
	for _, c := range []struct {
		name string
		rows [][]string
		want dbOutcome
	}{
		{"nullaggregat", [][]string{{"0"}}, dbEmpty},
		{"null-verdi", [][]string{{"NULL"}}, dbEmpty},
		{"ekte tall", [][]string{{"1473032.49"}}, dbOK},
	} {
		att := dbAttempt{outcome: dbOK, cols: cols, rows: c.rows}
		if len(c.rows) == 0 || zeroAggregate(cols, c.rows) {
			att.outcome = dbEmpty
		}
		if att.outcome != c.want {
			t.Errorf("%s: utfall %v, ventet %v", c.name, att.outcome, c.want)
		}
	}
}

// Prod-feilen: totaltall lyktes, Moestue-spørringen timet ut, svaret ble
// «0 kroner». Alle subjekt-spørringer feilet → vi vet ingenting om subjektet.
func TestSubjectFailedCatchesTimedOutSubject(t *testing.T) {
	attempts := []dbAttempt{
		{outcome: dbOK, sql: "SELECT SUM(net_revenue) FROM v_Sales"},
		{outcome: dbFailed, sql: "SELECT company, SUM(net_revenue) FROM v_Sales WHERE company LIKE '%Moestue%' GROUP BY company"},
	}
	subject, bad := subjectFailed("Hvor mye har Moestue & Cask AS kjøpt for?", attempts)
	if !bad || !strings.Contains(subject, "Moestue") {
		t.Fatalf("subjektfeil ikke fanget: %q, %v", subject, bad)
	}
}

// Lyktes ÉN subjekt-spørring, har modellen data og skal få svare.
func TestSubjectFailedAllowsPartialSuccess(t *testing.T) {
	attempts := []dbAttempt{
		{outcome: dbFailed, sql: "SELECT * FROM v_Sales WHERE company LIKE '%Moestue%'"},
		{outcome: dbOK, sql: "SELECT SUM(net_revenue) FROM v_Sales WHERE company LIKE '%Moestue%'"},
	}
	if _, bad := subjectFailed("Hvor mye har Moestue & Cask AS kjøpt for?", attempts); bad {
		t.Error("delvis suksess skal slippe gjennom")
	}
}

// Tomt resultat er et legitimt «ikke funnet» (fuzzy har alt kjørt) — ikke en
// teknisk feil, og skal ikke utløse porten.
func TestSubjectFailedIgnoresEmpty(t *testing.T) {
	attempts := []dbAttempt{
		{outcome: dbEmpty, sql: "SELECT SUM(x) FROM orders WHERE name LIKE '%Moestue%'"},
	}
	if _, bad := subjectFailed("Hva har Moestue kjøpt?", attempts); bad {
		t.Error("dbEmpty er ikke en teknisk feil")
	}
}

// Ingen subjekt-spørringer i det hele tatt: checkDenial eier det tilfellet.
func TestSubjectFailedNeedsTouchedQueries(t *testing.T) {
	attempts := []dbAttempt{{outcome: dbFailed, sql: "SELECT SUM(x) FROM orders"}}
	if _, bad := subjectFailed("Hva har Moestue kjøpt?", attempts); bad {
		t.Error("navn som aldri ble søkt på hører til benektelsesporten")
	}
}
