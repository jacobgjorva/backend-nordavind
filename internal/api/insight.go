package api

import (
	"fmt"
	"strings"
	"time"
)

// Innsiktslaget: det koden vet om resultatet, men aldri har sagt.
//
// Målt problem: assistenten svarte «Kundene med lavest kredittverdighet er de
// med kredittgrense på 0» — mens credit_limit var 0 for ALLE 4 674 kundene.
// Kolonnen var ubrukt, ikke lav. Koden visste det (constantColumn), men hvisket
// det bare til modellen. Samme sak med «Vi synker» målt på en halv måned mot en
// hel, og med resultater som stille stoppet på radgrensen.
//
// Her regnes observasjonen ut av rader vi ALLEREDE har i minnet, og leveres som
// én setning etter svaret. Null modellkall, null completion-tokens: koden
// skriver teksten. Setningen går utenom kildekontrollen med vilje — tall vi
// selv har regnet finnes ikke i verktøyresultatet, og gaten måler uansett bare
// modellens prosa (samme kontrakt som explainAttempts og tableBlock).
//
// Ny observasjon = én konstant, én detektor, én oppføring i insightPhrases.

type insightKind int

// REKKEFØLGEN ER PRIORITETEN: øverst det som gjør svaret direkte misvisende,
// nederst det som bare er verdt å vite.
const (
	insNone          insightKind = iota
	insConstantCol               // kolonnen skiller ikke radene — «rangeringen» er tom
	insDuplicates                // samme enhet flere ganger — rader, ikke enheter
	insPartialPeriod             // siste periode er ikke ferdig — sammenligningen halter
	insRowCap                    // stoppet på radgrensen — ikke hele lista
	insStale                     // ferskeste rad er gammel
	insConcentration             // få rader står for mesteparten
	insNulls                     // hull i en kolonne som faktisk ble brukt
)

// insight er én observasjon utledet av et resultatsett.
type insight struct {
	kind insightKind
	col  string // kolonnen observasjonen gjelder
	val  string // ferdigformatert nøkkelverdi («0», «41 %», «14. mars»)
	n    int    // rader observasjonen bygger på
	// other er den andre tellingen når typen trenger to: unike verdier
	// (duplikater), tomme felt (hull).
	other int
}

// maxInsightChars: «én setning ekstra» skal være nettopp det.
const maxInsightChars = 120

// staleAfterDays: ferskere enn dette er ikke verdt å kommentere.
const staleAfterDays = 45

// partialPeriodCutoff: fra denne dagen i måneden er det uvesentlig at måneden
// ikke er ferdig, og påminnelsen blir bare masete.
const partialPeriodCutoff = 25

// colProfile er én kolonne, typet ÉN gang og delt av alle detektorene.
type colProfile struct {
	name     string
	idx      int
	nums     []float64
	dates    []time.Time
	blanks   int
	distinct int
	numeric  bool
	dateish  bool
	idLike   bool // id/nr/år/kode — aldri et måltall
}

// observe leser ett resultatsett og gir den STERKESTE observasjonen, eller
// ingenting. sqlText og userText brukes kun til støydemping.
func observe(cols []string, rows [][]string, sqlText, userText string, rowCap int, now time.Time) (insight, bool) {
	if len(cols) == 0 || len(rows) == 0 {
		return insight{}, false
	}
	lowSQL := strings.ToLower(sqlText)
	lowUser := strings.ToLower(userText)
	ps := profileColumns(cols, rows)

	// Rekkefølgen her ER prioriteringen. Første treff vinner.
	if in, ok := findConstantCol(cols, rows, lowSQL, lowUser); ok {
		return in, true
	}
	if in, ok := findDuplicates(ps, rows, lowSQL); ok {
		return in, true
	}
	if in, ok := findPartialPeriod(ps, now); ok {
		return in, true
	}
	if in, ok := findRowCap(rows, rowCap, lowUser); ok {
		return in, true
	}
	if in, ok := findStale(ps, lowSQL, now); ok {
		return in, true
	}
	if in, ok := findConcentration(ps, rows, lowSQL); ok {
		return in, true
	}
	if in, ok := findNulls(ps, rows, lowSQL, lowUser); ok {
		return in, true
	}
	return insight{}, false
}

// --- detektorer -------------------------------------------------------------

// findConstantCol gjenbruker constantColumn, men slipper det bare videre til
// BRUKEREN når kolonnen faktisk ble brukt til noe. Ellers ville vi meldt «alle
// returer er 0» til en som spurte om omsetning.
func findConstantCol(cols []string, rows [][]string, lowSQL, lowUser string) (insight, bool) {
	col, val := constantColumn(cols, rows)
	if col == "" || !columnInPlay(col, lowSQL, lowUser) {
		return insight{}, false
	}
	shown := val
	if shown == "" {
		shown = "tom"
	}
	return insight{kind: insConstantCol, col: col, val: shown, n: len(rows)}, true
}

// findDuplicates fanger join-fanout: lista ser ut som enheter, men er rader.
func findDuplicates(ps []colProfile, rows [][]string, lowSQL string) (insight, bool) {
	n := len(rows)
	if n < 6 {
		return insight{}, false
	}
	for _, p := range ps {
		if p.numeric || p.dateish || p.idLike || p.distinct == 0 {
			continue
		}
		// Er det gruppert på kolonnen, er gjentakelse umulig og poenget dødt.
		if strings.Contains(lowSQL, "group by") && strings.Contains(lowSQL, strings.ToLower(p.name)) {
			return insight{}, false
		}
		if p.distinct*3 <= n*2 {
			return insight{kind: insDuplicates, col: p.name, n: n, other: p.distinct}, true
		}
		return insight{}, false // kun første tekstkolonne er interessant
	}
	return insight{}, false
}

// findPartialPeriod: sammenlignes måneder, og den siste er inneværende, måler
// vi en halv måned mot hele. Det er ikke en nyanse, det er feil konklusjon.
func findPartialPeriod(ps []colProfile, now time.Time) (insight, bool) {
	if now.Day() > partialPeriodCutoff {
		return insight{}, false
	}
	for _, p := range ps {
		if !p.dateish || len(p.dates) < 2 {
			continue
		}
		months := map[string]bool{}
		var newest time.Time
		for _, d := range p.dates {
			months[d.Format("2006-01")] = true
			if d.After(newest) {
				newest = d
			}
		}
		if len(months) < 2 {
			return insight{}, false
		}
		if newest.Year() == now.Year() && newest.Month() == now.Month() {
			return insight{kind: insPartialPeriod, col: p.name,
				val: norwegianMonth(newest), n: now.Day()}, true
		}
		return insight{}, false
	}
	return insight{}, false
}

// findRowCap: traff vi taket, så brukeren ikke hele lista.
func findRowCap(rows [][]string, rowCap int, lowUser string) (insight, bool) {
	if rowCap <= 0 || len(rows) != rowCap {
		return insight{}, false
	}
	// Ba brukeren selv om akkurat dette antallet, er grensen ikke en overraskelse.
	if strings.Contains(lowUser, fmt.Sprintf("%d", rowCap)) {
		return insight{}, false
	}
	return insight{kind: insRowCap, n: rowCap}, true
}

// findStale: ferskeste rad er gammel. Undertrykkes når spørringen selv
// avgrenset tidsrommet — da er «dataene stopper i 2024» en fornærmelse.
func findStale(ps []colProfile, lowSQL string, now time.Time) (insight, bool) {
	for _, p := range ps {
		if !p.dateish || len(p.dates) == 0 {
			continue
		}
		var newest time.Time
		for _, d := range p.dates {
			if d.After(newest) {
				newest = d
			}
		}
		if now.Sub(newest) <= staleAfterDays*24*time.Hour {
			return insight{}, false
		}
		if deliberateDateFilter(lowSQL, newest) {
			return insight{}, false
		}
		return insight{kind: insStale, col: p.name, val: norwegianDate(newest)}, true
	}
	return insight{}, false
}

// findConcentration er «den drevne kollega»-observasjonen: tallet stemmer, men
// fordelingen bak er skjev. Under terskelen er «ganske jevnt fordelt» fyll.
func findConcentration(ps []colProfile, rows [][]string, lowSQL string) (insight, bool) {
	if len(rows) < 5 {
		return insight{}, false
	}
	p, ok := measureColumn(ps)
	if !ok || len(p.nums) != len(rows) {
		return insight{}, false
	}
	var sum, top1, top3 float64
	for i, v := range p.nums {
		if v < 0 {
			return insight{}, false // negative tall gir ingen meningsfull andel
		}
		sum += v
		if i == 0 {
			top1 = v
		}
		if i < 3 {
			top3 += v
		}
	}
	if sum <= 0 {
		return insight{}, false
	}
	// Bare en rangering har en «topp». Er resultatet usortert, betyr rad 1 intet.
	if !strings.Contains(lowSQL, "order by") && !isDescending(p.nums) {
		return insight{}, false
	}
	// NB på terskelen for topp tre: med n helt like rader er top3/sum = 3/n,
	// som alene gir 0,60 ved n=5. Uten radkravet under ville en perfekt jevn
	// femrading blitt meldt som «skjev».
	switch {
	case top1/sum >= 0.35:
		return insight{kind: insConcentration, col: p.name,
			val: pct(top1 / sum), n: len(rows)}, true
	case len(rows) >= 8 && top3/sum >= 0.65:
		return insight{kind: insConcentration, col: p.name,
			val: pct(top3 / sum), n: 3}, true
	}
	return insight{}, false
}

// findNulls: hull i en kolonne brukeren faktisk brydde seg om.
func findNulls(ps []colProfile, rows [][]string, lowSQL, lowUser string) (insight, bool) {
	n := len(rows)
	if n < 8 {
		return insight{}, false
	}
	for _, p := range ps {
		if p.blanks == 0 || p.blanks == n { // helt full eller helt tom (= constantCol)
			continue
		}
		if p.blanks*10 < n*3 { // under 30 %
			continue
		}
		if !columnInPlay(p.name, lowSQL, lowUser) {
			continue
		}
		return insight{kind: insNulls, col: p.name, n: n, other: p.blanks}, true
	}
	return insight{}, false
}

// --- profilering ------------------------------------------------------------

func profileColumns(cols []string, rows [][]string) []colProfile {
	out := make([]colProfile, 0, len(cols))
	for i, name := range cols {
		p := colProfile{name: name, idx: i, idLike: looksLikeID(name)}
		seen := map[string]bool{}
		numOK, dateOK := 0, 0
		for _, r := range rows {
			if i >= len(r) {
				continue
			}
			v := strings.TrimSpace(r[i])
			if v == "" || strings.EqualFold(v, "null") {
				p.blanks++
				continue
			}
			if !seen[v] {
				seen[v] = true
			}
			if f, ok := parseLooseNumber(v); ok {
				numOK++
				p.nums = append(p.nums, f)
			}
			if d, ok := parseLooseDate(v); ok {
				dateOK++
				p.dates = append(p.dates, d)
			}
		}
		p.distinct = len(seen)
		filled := len(rows) - p.blanks
		// En kolonne er av en type når alle utfylte verdier er det. Datoer
		// sjekkes først: «2026» parser også som tall.
		p.dateish = filled > 0 && dateOK == filled
		p.numeric = filled > 0 && numOK == filled && !p.dateish
		out = append(out, p)
	}
	return out
}

// measureColumn velger måltallet: siste tallkolonne som ikke er en id. I
// «customer_name, order_count, total_spent» er det total_spent.
func measureColumn(ps []colProfile) (colProfile, bool) {
	for i := len(ps) - 1; i >= 0; i-- {
		if ps[i].numeric && !ps[i].idLike {
			return ps[i], true
		}
	}
	return colProfile{}, false
}

var idWords = []string{"id", "nr", "nummer", "number", "kode", "code", "år", "year", "mnd", "month"}

func looksLikeID(name string) bool {
	l := strings.ToLower(name)
	for _, w := range idWords {
		if l == w || strings.HasSuffix(l, "_"+w) || strings.HasPrefix(l, w+"_") {
			return true
		}
	}
	return false
}

var dateLayouts = []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339, "02.01.2006", "2006-01"}

func parseLooseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 7 {
		return time.Time{}, false
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	// «2026-07-01T00:00:00Z» og varianter med tidssone-hale.
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// --- hjelpere ---------------------------------------------------------------

// columnInPlay: brydde noen seg om denne kolonnen? Enten sorterte vi på den,
// eller så nevnte brukeren den. Ellers er en observasjon om den bare støy.
func columnInPlay(col, lowSQL, lowUser string) bool {
	l := strings.ToLower(col)
	// Sortert på den: åpenbart i bruk.
	if i := strings.Index(lowSQL, "order by"); i >= 0 && strings.Contains(lowSQL[i:], l) {
		return true
	}
	// Filtrert på den: like sterkt signal. Dette er kredittgrense-tilfellet —
	// «WHERE credit_limit = 0» uten ORDER BY, og brukeren spurte på norsk
	// («kredittverdighet»), som aldri matcher det engelske kolonnenavnet.
	if i := strings.Index(lowSQL, "where"); i >= 0 && strings.Contains(lowSQL[i:], l) {
		return true
	}
	// Kolonnenavn er ofte sammensatt (credit_limit); prøv ordene hver for seg.
	for _, part := range strings.Split(l, "_") {
		if len([]rune(part)) >= 4 && strings.Contains(lowUser, part) {
			return true
		}
	}
	return strings.Contains(lowUser, l)
}

// deliberateDateFilter: avgrenset spørringen tidsrommet med vilje?
func deliberateDateFilter(lowSQL string, newest time.Time) bool {
	if strings.Contains(lowSQL, "between") {
		return true
	}
	if strings.Contains(lowSQL, fmt.Sprintf("%d", newest.Year())) {
		return true
	}
	return strings.Contains(lowSQL, "<=") || strings.Contains(lowSQL, "< '")
}

func isDescending(v []float64) bool {
	for i := 1; i < len(v); i++ {
		if v[i] > v[i-1] {
			return false
		}
	}
	return true
}

func pct(f float64) string {
	return fmt.Sprintf("%.0f %%", f*100)
}

var norwegianMonths = []string{"januar", "februar", "mars", "april", "mai", "juni",
	"juli", "august", "september", "oktober", "november", "desember"}

func norwegianMonth(t time.Time) string {
	return norwegianMonths[int(t.Month())-1]
}

func norwegianDate(t time.Time) string {
	return fmt.Sprintf("%d. %s", t.Day(), norwegianMonth(t))
}

// --- setningene -------------------------------------------------------------

// insightPhrases er søsterregisteret til `narration` i narrate.go: flere
// varianter per type, valgt med samme deterministiske rotasjon.
//
// Tonekontrakt: OBSERVERENDE, aldri rådgivende. Vi sier hva som står i
// dataene, aldri «du bør». Tørr humor kun der virkeligheten fortjener den —
// radgrensen og den ubrukte kolonnen. Resten er nøkterne, ellers slites
// glimtet ut. Maks 120 tegn: «én setning ekstra» skal være det.
var insightPhrases = map[insightKind]func(n *narrator, in insight) string{
	insConstantCol: func(n *narrator, in insight) string {
		if in.val == "tom" {
			return n.pick(
				fmt.Sprintf("Merk at %s er tom i alle radene — feltet er rett og slett ikke i bruk.", in.col),
				fmt.Sprintf("Kolonnen %s står tom hos samtlige, så der er det ingenting å hente.", in.col),
			)
		}
		return n.pick(
			fmt.Sprintf("Merk at %s er %s i alle radene — den kolonnen rangerer ingenting.", in.col, in.val),
			fmt.Sprintf("Verdt å vite: %s står til %s hos samtlige, så det er ikke lavt, det er ubrukt.", in.col, in.val),
			fmt.Sprintf("Kolonnen %s er %s overalt her, så rekkefølgen betyr ingenting.", in.col, in.val),
		)
	},
	insDuplicates: func(n *narrator, in insight) string {
		return n.pick(
			fmt.Sprintf("Lista har %s rader, men bare %s unike — dette er rader, ikke enheter.", nf(in.n), nf(in.other)),
			fmt.Sprintf("%s rader fordeler seg på %s unike verdier, så noen går igjen.", nf(in.n), nf(in.other)),
		)
	},
	insPartialPeriod: func(n *narrator, in insight) string {
		return n.pick(
			fmt.Sprintf("%s er ikke ferdig ennå, så det er en halv måned målt mot hele.", capitalize(in.val)),
			fmt.Sprintf("Husk at vi bare er %d dager inn i %s — perioden er ikke sammenlignbar ennå.", in.n, in.val),
		)
	},
	insRowCap: func(n *narrator, in insight) string {
		return n.pick(
			fmt.Sprintf("Dette er de %s første radene, som er grensen min — det ligger trolig flere bak.", nf(in.n)),
			fmt.Sprintf("Jeg stoppet på %s rader, så dette er neppe hele bildet.", nf(in.n)),
		)
	},
	insStale: func(n *narrator, in insight) string {
		return n.pick(
			fmt.Sprintf("Ferskeste rad her er fra %s, så de siste månedene mangler.", in.val),
			fmt.Sprintf("Dataene stopper %s — noe ser ut til å ikke ha kommet inn siden.", in.val),
		)
	},
	insConcentration: func(n *narrator, in insight) string {
		if in.n == 3 {
			return n.pick(
				fmt.Sprintf("Fordelingen er skjev: de tre øverste tar %s av totalen.", in.val),
				fmt.Sprintf("Merk at topp tre alene står for %s her.", in.val),
			)
		}
		return n.pick(
			fmt.Sprintf("Fordelingen er skjev: den øverste tar %s av totalen alene.", in.val),
			fmt.Sprintf("Verdt å merke seg at nummer én står for %s av dette.", in.val),
		)
	},
	insNulls: func(n *narrator, in insight) string {
		return n.pick(
			fmt.Sprintf("Merk at %s mangler i %s av %s rader.", in.col, nf(in.other), nf(in.n)),
			fmt.Sprintf("Kolonnen %s er tom i %s av %s rader, så bildet er ufullstendig.", in.col, nf(in.other), nf(in.n)),
		)
	},
}

// insightSentence bygger setningen, eller tom streng.
func insightSentence(n *narrator, in insight) string {
	f, ok := insightPhrases[in.kind]
	if !ok || f == nil {
		return ""
	}
	s := strings.TrimSpace(f(n, in))
	if len([]rune(s)) > maxInsightChars {
		return ""
	}
	return s
}

// deliverInsight sender ÉN kort observasjon etter svaret. Teksten er REGNET av
// oss på rader vi har i minnet, ikke gjengitt av en modell — den finnes derfor
// ikke i verktøyresultatet, og skal med vilje ikke gjennom kildekontrollen.
// Samme kontrakt som explainAttempts og tableBlock.
//
// Portene her er det som skiller en dreven kollega fra en som maser.
func (n *narrator) deliverInsight(emit func(string), in insight, answer string, limit int, listShown bool) {
	// Kun vanlig chat: utfører-flytene har egne kontrakter der prat er feil.
	if n == nil || !n.on || in.kind == insNone {
		return
	}
	// «Det ligger flere bak» gir bare mening hvis brukeren faktisk så en liste.
	// Målt: svaret «margindata finnes ikke» fikk påhengt «Jeg stoppet på 100
	// rader» fordi en utforskende spørring i samme tur traff taket.
	if in.kind == insRowCap && !listShown {
		return
	}
	answer = strings.TrimSpace(answer)
	// Modellen ba om oppklaring. En observasjon etter et spørsmål leser som at
	// vi ikke hørte etter.
	if strings.HasSuffix(answer, "?") {
		return
	}
	// Merk: her sto det tidligere en deniedRe-sjekk på svarteksten. Den ble
	// fjernet — modellen finner stadig nye måter å si «finnes ikke» på, og
	// samme skjørhet felte den første benektelsesporten. Problemet løses i
	// stedet ved kilden: `pending` nullstilles når en senere spørring ikke gir
	// observasjon, så innsikten alltid tilhører resultatet svaret bygger på.
	s := insightSentence(n, in)
	if s == "" {
		return
	}
	low := strings.ToLower(answer)
	// Sa modellen det allerede? Da er vi bare irriterende.
	if in.col != "" && strings.Contains(low, strings.ToLower(in.col)) {
		return
	}
	if in.val != "" {
		if d := normDigits(in.val); d != "" && strings.Contains(normDigits(answer), d) {
			return
		}
	}
	// Plassbudsjettet fra flyt-tabellen. Dette er første gang MaxChars faktisk
	// gjør en jobb — til nå havnet den bare i en loggsetning.
	//
	// Ble en tabell vist, ER tabellen svaret, og prosaen skal være kort. Det
	// var hele forskjellen mellom de tidligere flytene data_question og
	// show_table; nå avledes den av hva som faktisk skjedde i stedet for av
	// hva ruteren gjettet på forhånd.
	if listShown && limit > 150 {
		limit = 150
	}
	if limit > 0 && len([]rune(answer))+len([]rune(s))+1 > limit {
		return
	}
	emit(contentSSE(" " + s))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
