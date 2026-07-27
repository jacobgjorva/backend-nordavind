package api

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
)

// Utholdenhet i kode, ikke i prompt.
//
// Målt problem: modellen fikk PROSA tilbake fra verktøyene, så «tabellen
// finnes ikke», «null rader» og «timeout» så helt like ut. Da ga den enten
// opp («Racamca AB finnes ikke») eller substituerte stille noe i nærheten
// («de dyreste produktene» besvart med ordrer). Begge deler leser som en
// slappfisk.
//
// Her får hvert databasekall et TYPET utfall, og koden prøver selv neste vei
// før modellen får svaret: nærmeste navn ved bom på fritekst, den faktiske
// tabell-lista ved manglende tilgang, og et varsel når en kolonne er konstant
// og sorteringen derfor er meningsløs. Null tokens — dette er spørringer og
// strengbygging, ikke modellkall.

type dbOutcome int

const (
	dbOK       dbOutcome = iota // rader kom tilbake
	dbEmpty                     // kjørte fint, men null rader
	dbNoAccess                  // tabell/kolonne er ikke tilgjengelig
	dbFailed                    // teknisk feil: timeout, syntaks, tilkobling
)

func (o dbOutcome) String() string {
	switch o {
	case dbOK:
		return "ok"
	case dbEmpty:
		return "tomt"
	case dbNoAccess:
		return "ingen tilgang"
	default:
		return "feilet"
	}
}

// dbAttempt er ett databaseforsøk med utfallet gjort eksplisitt.
type dbAttempt struct {
	outcome dbOutcome
	sql     string
	conn    string
	text    string // det modellen faktisk får se
	cols    []string
	rows    [][]string
	// note er kodens egen observasjon (nærmeste navn, konstant kolonne …),
	// lagt til teksten modellen ser.
	note string
}

func (a dbAttempt) ok() bool { return a.outcome == dbOK }

// --- Strategi 1: nærmeste navn når et fritekstfilter bommer -----------------

var (
	// WHERE-mønstre vi kan lete videre fra: kolonne sammenlignet med en
	// tekstverdi, med eller uten lower()/LIKE og jokertegn.
	eqFilterRe   = regexp.MustCompile(`(?i)(?:lower\s*\(\s*)?([a-z0-9_."]+)\s*\)?\s*(?:=|like|ilike)\s*'([^']*)'`)
	fromTableRe  = regexp.MustCompile(`(?i)\bfrom\s+([a-z0-9_."]+)`)
	nonWordRe    = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	trailingPctS = "%"
)

// nearestValues slår opp verdier som ligner det modellen søkte etter.
// Strategien er bevisst billig: prefikssøk på de første tegnene (stavefeil
// sitter nesten alltid inne i eller sist i ordet), deretter rangering på
// redigeringsavstand i Go. Ett lett kall, ingen full skanning.
func (s *Server) nearestValues(ctx context.Context, db *sql.DB, dc dbConn, query string) (col, needle string, hits []string) {
	m := eqFilterRe.FindStringSubmatch(query)
	if m == nil {
		return "", "", nil
	}
	col = strings.Trim(strings.ToLower(m[1]), `"`)
	needle = strings.Trim(strings.TrimSpace(m[2]), "%")
	if len([]rune(needle)) < 3 || col == "" {
		return "", "", nil
	}
	// Tall og datoer har ingen «nærmeste stavemåte» — ikke gjett der.
	if strings.IndexFunc(needle, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) < 0 {
		return "", "", nil
	}
	tm := fromTableRe.FindStringSubmatch(query)
	if tm == nil {
		return "", "", nil
	}
	table := strings.Trim(strings.ToLower(tm[1]), `"`)
	if i := strings.LastIndex(table, "."); i >= 0 {
		table = table[i+1:]
	}
	if !tableAllowed(dc, table) {
		return "", "", nil
	}
	// Kolonnenavnet kan komme med tabellalias (c.customer_name).
	if i := strings.LastIndex(col, "."); i >= 0 {
		col = col[i+1:]
	}
	if !safeIdent(col) || !safeIdent(table) {
		return "", "", nil
	}

	// Prefiks: de første tegnene i det lengste ordet i søket.
	word := longestWord(needle)
	prefix := word
	if r := []rune(prefix); len(r) > 4 {
		prefix = string(r[:4])
	}
	q := fmt.Sprintf(
		`SELECT DISTINCT %s FROM %s WHERE lower(%s) LIKE '%s' AND %s IS NOT NULL`,
		col, table, col, strings.ToLower(prefix)+trailingPctS, col)
	cols, rows, err := connector.SafeQueryViewsN(ctx, db, dc.creds.Driver, q, dc.allowed, dc.views, 40)
	if err != nil || len(cols) == 0 {
		return col, needle, nil
	}
	var cands []string
	for _, r := range rows {
		if len(r) > 0 && strings.TrimSpace(r[0]) != "" {
			cands = append(cands, r[0])
		}
	}
	return col, needle, rankByCloseness(needle, cands, 3)
}

// rankByCloseness sorterer kandidatene etter redigeringsavstand til søket og
// returnerer de n nærmeste. Kandidater som er vilt forskjellige forkastes —
// et dårlig forslag er verre enn ingen.
func rankByCloseness(needle string, cands []string, n int) []string {
	type scored struct {
		val  string
		dist int
	}
	nl := strings.ToLower(needle)
	var out []scored
	for _, c := range cands {
		d := levenshtein(nl, strings.ToLower(c))
		// Godta bare treff som er klart i nærheten av søket.
		if limit := len([]rune(nl))/2 + 2; d <= limit {
			out = append(out, scored{c, d})
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].dist < out[j-1].dist; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	var vals []string
	for i, s := range out {
		if i >= n {
			break
		}
		vals = append(vals, s.val)
	}
	return vals
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func longestWord(s string) string {
	best := ""
	for _, w := range nonWordRe.Split(s, -1) {
		if len([]rune(w)) > len([]rune(best)) {
			best = w
		}
	}
	return best
}

// --- Strategi 2: er kolonnen vi sorterte på i det hele tatt i bruk? ---------

// constantColumn sier fra når hele resultatet har samme verdi i en kolonne
// brukeren tydeligvis ville skille på. «Kundene med lavest kredittgrense»
// er meningsløst når credit_limit er 0 for alle — det skal sies, ikke pyntes
// over. Returnerer kolonnenavn og verdien, eller tomt.
func constantColumn(cols []string, rows [][]string) (string, string) {
	if len(rows) < 3 || len(cols) == 0 {
		return "", ""
	}
	for i := range cols {
		first := ""
		same := true
		for r, row := range rows {
			if i >= len(row) {
				same = false
				break
			}
			if r == 0 {
				first = row[i]
				continue
			}
			if row[i] != first {
				same = false
				break
			}
		}
		// Bare interessant for tallkolonner og tomme felt — at alle kunder
		// står i samme land er sjelden en feil.
		if same && (first == "" || first == "0" || first == "0.00" || first == "false") {
			return cols[i], first
		}
	}
	return "", ""
}

// --- hjelpere ---------------------------------------------------------------

func tableAllowed(dc dbConn, table string) bool {
	for _, t := range dc.allowed {
		if strings.EqualFold(t, table) {
			return true
		}
	}
	_, ok := dc.views[strings.ToLower(table)]
	return ok
}

// safeIdent slipper bare gjennom rene identifikatorer — det vi bygger inn i
// hjelpespørringene er aldri brukerstyrt tekst, men vi tar ingen sjanser.
func safeIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// lastAttempt gir siste databaseforsøk, eller et tomt.
func lastAttempt(as []dbAttempt) dbAttempt {
	if len(as) == 0 {
		return dbAttempt{}
	}
	return as[len(as)-1]
}

// anyOK: fikk vi noen gang faktiske rader denne turen?
func anyOK(as []dbAttempt) bool {
	for _, a := range as {
		if a.ok() {
			return true
		}
	}
	return false
}

// --- Kvalitetsporten --------------------------------------------------------

// explainAttempts bygger den ærlige redegjørelsen når turen ikke ga noe
// håndfast. Den erstatter den gamle faste teksten «Databasen svarte ikke i
// tide», som var en usannhet i de fleste tilfellene: basen svarte som regel
// umiddelbart, men på noe modellen ikke hadde tilgang til.
//
// Formen er kollegial og konkret: hva jeg prøvde, hvorfor det ikke gikk, hva
// jeg faktisk har, og hva jeg trenger fra deg. Skrevet i kode — null tokens.
func explainAttempts(attempts []dbAttempt, available []string) string {
	if len(attempts) == 0 {
		return "Jeg fikk ikke gjort noe oppslag i databasen på dette. Prøv igjen om et lite øyeblikk."
	}
	var noAccess, empty, failed int
	for _, a := range attempts {
		switch a.outcome {
		case dbNoAccess:
			noAccess++
		case dbEmpty:
			empty++
		case dbFailed:
			failed++
		}
	}

	var b strings.Builder
	switch {
	case failed > 0 && noAccess == 0 && empty == 0:
		b.WriteString("Databasen svarte ikke i tide, så jeg har ingen tall å gi deg akkurat nå. ")
		b.WriteString("Prøv igjen om et lite øyeblikk.")
		return b.String()

	case noAccess > 0:
		b.WriteString("Det jeg trenger for å svare på dette ligger ikke i dataene jeg har tilgang til. ")
		if len(available) > 0 {
			b.WriteString("Jeg har " + humanList(available) + ". ")
		}
		b.WriteString("Si fra hvis noe av det kan brukes i stedet, eller få lagt til kilden som mangler.")
		return b.String()

	default:
		b.WriteString("Jeg gjorde ")
		if len(attempts) == 1 {
			b.WriteString("ett oppslag")
		} else {
			b.WriteString(fmt.Sprintf("%d oppslag", len(attempts)))
		}
		b.WriteString(" i databasen, men ingen av dem ga rader. ")
		if len(available) > 0 {
			b.WriteString("Det jeg har er " + humanList(available) + ". ")
		}
		b.WriteString("Vet du hva det heter i deres system, eller omtrent når det skjedde, finner jeg det.")
		return b.String()
	}
}

// humanList setter sammen en liste slik en person ville sagt den.
func humanList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " og " + items[1]
	}
	if len(items) > 6 {
		return strings.Join(items[:6], ", ") + " med flere"
	}
	return strings.Join(items[:len(items)-1], ", ") + " og " + items[len(items)-1]
}

// availableTables lister det modellen faktisk kan spørre om, på tvers av
// tilkoblingene. Grunnlaget for å si «dette har jeg» i stedet for bare «nei».
func availableTables(t *dbToolCtx) []string {
	if t == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, dc := range t.conns {
		for _, name := range dc.allowed {
			if k := strings.ToLower(name); !seen[k] {
				seen[k] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// --- Strategi 3: gjør SQL-feil selvhjulpne ---------------------------------

var (
	unknownColRe = regexp.MustCompile(`(?i)column "?([a-z0-9_.]+)"? does not exist`)
	ambiguousRe  = regexp.MustCompile(`(?i)column reference "?([a-z0-9_.]+)"? is ambiguous`)
)

// sqlFixHint gjør en rå databasefeil om til konkret veiledning med de FAKTISKE
// kolonnene. Uten dette gjentok modellen samme bom: vi målte tre identiske
// «column ... is ambiguous» på rad, fordi feilmeldingen ikke sa hva den skulle
// gjort i stedet. Returnerer tom streng når vi ikke har noe nyttig å tilføye.
func sqlFixHint(errMsg, query string, dc dbConn) string {
	if m := ambiguousRe.FindStringSubmatch(errMsg); m != nil {
		col := m[1]
		var owners []string
		for _, tbl := range tablesInQuery(query, dc) {
			if hasColumn(dc, tbl, col) {
				owners = append(owners, tbl+"."+col)
			}
		}
		hint := fmt.Sprintf("Kolonnen %q finnes i flere av tabellene du joiner, så den må ha "+
			"tabellprefiks.", col)
		if len(owners) > 0 {
			hint += " Velg én: " + strings.Join(owners, " eller ") + "."
		}
		hint += " Husk prefiks også i GROUP BY og ORDER BY."
		return hint
	}

	if m := unknownColRe.FindStringSubmatch(errMsg); m != nil {
		want := m[1]
		if i := strings.LastIndex(want, "."); i >= 0 {
			want = want[i+1:]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Kolonnen %q finnes ikke. Dette er kolonnene du faktisk har:", want)
		found := false
		for _, tbl := range tablesInQuery(query, dc) {
			if cols := dc.columns[strings.ToLower(tbl)]; len(cols) > 0 {
				fmt.Fprintf(&b, "\n%s: %s", tbl, strings.Join(cols, ", "))
				found = true
			}
		}
		if !found {
			return ""
		}
		// Nærmeste kolonnenavn er ofte det modellen mente.
		var all []string
		for _, tbl := range tablesInQuery(query, dc) {
			all = append(all, dc.columns[strings.ToLower(tbl)]...)
		}
		if near := rankByCloseness(want, all, 2); len(near) > 0 {
			fmt.Fprintf(&b, "\nMente du %s?", humanList(near))
		}
		b.WriteString("\nSkriv spørringen på nytt med et navn fra lista. Finnes ikke det du " +
			"trenger, si det rett ut til brukeren i stedet for å bytte til noe annet.")
		return b.String()
	}
	return ""
}

// tablesInQuery gir de tillatte tabellene spørringen faktisk refererer til.
func tablesInQuery(query string, dc dbConn) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range connector.ReferencedTables(query) {
		if seen[r] || !tableAllowed(dc, r) {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func hasColumn(dc dbConn, table, col string) bool {
	if i := strings.LastIndex(col, "."); i >= 0 {
		col = col[i+1:]
	}
	for _, c := range dc.columns[strings.ToLower(table)] {
		if strings.EqualFold(c, col) {
			return true
		}
	}
	return false
}
