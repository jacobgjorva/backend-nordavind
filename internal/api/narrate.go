package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Arbeidsnarrasjon: fremdriftsteksten brukeren ser mens assistenten jobber,
// skrevet DETERMINISTISK i kode. Modellen skriver ingenting av dette, så det
// koster null tokens (målt: modellen produserer 0 tegn prosa i verktøyrunder,
// og promptene forbyr den med vilje).
//
// Tonen er dreven og observant: si hva som faktisk skjer og hva som faktisk
// kom tilbake, aldri «behandler forespørsel». Sarkasmen er sjelden og spares
// til når virkeligheten fortjener den (tomme treff, trege baser) — den slites
// ut hvis den kommer hver gang.
//
// Nytt verktøy = én oppføring i `narration`. Ikke rør agent.go.

// narrArgs er verktøyargumentene narrasjonen kan lene seg på. Alle verktøy
// deler denne; ubrukte felt er bare tomme.
type narrArgs struct {
	Query        string `json:"query"`
	URL          string `json:"url"`
	SQL          string `json:"sql"`
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	FileID       string `json:"file_id"`
	MailID       string `json:"mail_id"`
}

// narrRes er verktøyresultatet, ferdig tolket så oppføringene slipper å parse.
type narrRes struct {
	raw  string
	rows int  // rader i et tabellsvar
	cols int  // kolonner i et tabellsvar
	hits int  // treff i et liste-/søkesvar
	ok   bool // svaret var strukturert og brukbart
}

// narrStep er de tre fasene et verktøy kan snakke i: før kallet, når det
// drøyer, og når resultatet er inne. Alle er valgfrie.
type narrStep struct {
	// kind styrer ikonet i tidslinjen. Frontend faller tilbake til søkeikonet
	// for ukjente verdier, så nye typer er trygge å legge til.
	kind   string
	before func(n *narrator, a narrArgs) string
	slow   []string // vises hvis kallet varer lenger enn slowAfter (roteres)
	after  func(n *narrator, r narrRes) string
}

// Stegtyper. Frontend mapper disse til ikoner.
const (
	kindDB    = "db"
	kindWeb   = "web"
	kindFile  = "file"
	kindMail  = "mail"
	kindTable = "table"
	kindAgent = "agent"
	kindLink  = "link"
)

// slowAfter er hvor lenge et verktøy får jobbe i stillhet før vi sier fra.
const slowAfter = 4 * time.Second

// narrator sender stegene til klienten som {"nordavind_step": "..."}.
// `on` skiller vanlig chat fra utfører-flytene (widget, deck, agent-oppsett,
// connector): der går bare den korte «hva jeg gjør nå»-kvitteringen, aldri
// funn- og ventemeldingene.
type narrator struct {
	emit func(string)
	on   bool
	last string
	// turn teller steg i turen og brukes til å variere formuleringer
	// deterministisk — samme verktøy to ganger på rad skal ikke gi samme ord.
	turn int
	// connName slår opp visningsnavnet til en databasetilkobling.
	connName func(id string) string
}

func newNarrator(emit func(string), on bool, connName func(string) string) *narrator {
	if connName == nil {
		connName = func(string) string { return "" }
	}
	return &narrator{emit: emit, on: on, connName: connName}
}

// say sender ett steg. Tomme og gjentatte steg svelges. Teksten ligger i
// nordavind_step som før (gamle klienter er uberørt); typen er et tillegg.
func (n *narrator) say(text, kind string) {
	if n == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" || text == n.last {
		return
	}
	n.last = text
	n.turn++
	payload := map[string]any{"nordavind_step": text}
	if kind != "" {
		payload["nordavind_step_kind"] = kind
	}
	meta, _ := json.Marshal(payload)
	n.emit("data: " + string(meta))
}

// pick velger deterministisk mellom formuleringer, så gjentatte kall av samme
// verktøy ikke leser som en gjentakelse.
func (n *narrator) pick(variants ...string) string {
	if len(variants) == 0 {
		return ""
	}
	return variants[n.turn%len(variants)]
}

// before sier hva vi er i ferd med å gjøre. Dette er kvitteringen som alltid
// har vært der (også i widget-, deck- og agent-flytene), så den går uansett
// modus — det er `slow` og `after` som er den nye, rike narrasjonen.
func (n *narrator) before(tool, rawArgs string) {
	if n == nil {
		return
	}
	step, ok := narration[tool]
	if !ok || step.before == nil {
		return
	}
	n.say(step.before(n, parseNarrArgs(rawArgs)), step.kind)
}

// slow melder fra hvis kallet drøyer. Returnerer en stopp-funksjon som MÅ
// kalles når verktøyet er ferdig (samme kontrakt som emitAfter).
func (n *narrator) slow(tool string) func() {
	if n == nil || !n.on {
		return func() {}
	}
	step, ok := narration[tool]
	if !ok || len(step.slow) == 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(slowAfter):
			n.say(n.pick(step.slow...), step.kind)
		}
	}()
	return func() { close(done) }
}

// after sier hva vi fant. Dette er hele poenget: brukeren skal se funnet, ikke
// bare at noe ble gjort.
func (n *narrator) after(tool, result string) {
	if n == nil || !n.on {
		return
	}
	step, ok := narration[tool]
	if !ok || step.after == nil {
		return
	}
	n.say(step.after(n, parseNarrRes(result)), step.kind)
}

// afterDB er `after` for databasekall, der utfallet er kjent eksakt. Uten
// dette måtte narrasjonen gjette fra resultatteksten, og den gjettet feil:
// «tabellen er ikke tilgjengelig» ble meldt som «null rader», altså en
// usannhet om hva som skjedde.
func (n *narrator) afterDB(a dbAttempt, raw string) {
	if n == nil || !n.on {
		return
	}
	switch a.outcome {
	case dbNoAccess:
		n.say(n.pick(
			"Den tabellen har jeg ikke tilgang til. Ser hva jeg faktisk har.",
			"Ikke tilgang der. Prøver med det jeg har.",
		), kindDB)
		return
	case dbFailed:
		n.say(n.pick(
			"Spørringen gikk ikke gjennom. Prøver en annen vei.",
			"Det kallet feilet. Omformulerer.",
		), kindDB)
		return
	}
	// Fant koden en nærmeste verdi, er DET funnet — ikke at raden manglet.
	if a.note != "" && a.outcome == dbEmpty {
		n.say(n.pick(
			"Ingen treff på det navnet, men noe ligner. Prøver på nytt.",
			"Traff ikke, men jeg ser hva det sannsynligvis het. Kjører om.",
		), kindDB)
		return
	}
	n.after("query_database", raw)
}

// afterHits er `after` for verktøy der kalleren allerede vet antallet og det
// er mer sannferdig enn å telle i resultatteksten (f.eks. antall søkekilder).
func (n *narrator) afterHits(tool string, hits int) {
	if n == nil || !n.on {
		return
	}
	step, ok := narration[tool]
	if !ok || step.after == nil {
		return
	}
	n.say(step.after(n, narrRes{hits: hits, ok: true}), step.kind)
}

// narration er registeret. Én oppføring per verktøy.
//
// Hver formulering har flere varianter: n.pick roterer deterministisk på
// stegtelleren, så to like kall etter hverandre aldri leser likt. Miksen er
// bevisst ujevn — noen varianter er helt nøkterne, så de tørre kommentarene
// treffer når de kommer i stedet for å bli maniert.
var narration = map[string]narrStep{
	"query_database": {
		kind: kindDB,
		before: func(n *narrator, a narrArgs) string {
			who := "databasen"
			if name := n.connName(a.ConnectionID); name != "" {
				who = name
			}
			what := describeSQL(a.SQL)
			if what == "" {
				return n.pick("Spør "+who+".", "Tar en tur innom "+who+".", "Henter fra "+who+".")
			}
			return n.pick(
				"Spør "+who+" om "+what+".",
				"Ber "+who+" om "+what+".",
				"Henter "+what+" fra "+who+".",
			)
		},
		slow: []string{
			"Basen tenker fortsatt. Jeg venter.",
			"Fortsatt stille fra databasen.",
			"Basen tar seg god tid i dag.",
			"Ingenting ennå. Databasen er visst opptatt med noe viktigere.",
		},
		after: func(n *narrator, r narrRes) string {
			if !r.ok {
				return n.pick(
					"Basen svarte ikke som forventet. Prøver en annen vei.",
					"Det svaret var ikke til å bli klok på. Ny vinkel.",
				)
			}
			switch r.rows {
			case 0:
				return n.pick(
					"Null rader. Enten er det tomt der inne, eller så stilte jeg feil spørsmål.",
					"Ingen treff. Databasen står ved sin sannhet: ingenting.",
					"Tomt. Det var lite hjelpsomt.",
					"Null rader tilbake. Jeg prøver en annen vei.",
				)
			case 1:
				return n.pick("Én rad. Kort og greit.", "Nøyaktig én rad. Konsist.", "Én rad tilbake.")
			}
			if r.rows >= 1000 {
				return n.pick(
					fmt.Sprintf("%s rader. Litt av en bunke.", nf(r.rows)),
					fmt.Sprintf("%s rader tilbake. Her var det materiale.", nf(r.rows)),
					fmt.Sprintf("%s rader. Går gjennom dem.", nf(r.rows)),
				)
			}
			return n.pick(
				fmt.Sprintf("%s rader tilbake. Ser på tallene.", nf(r.rows)),
				fmt.Sprintf("%s rader. Leser dem nå.", nf(r.rows)),
				fmt.Sprintf("Fikk %s rader. Går gjennom dem.", nf(r.rows)),
			)
		},
	},

	"show_table": {
		kind: kindTable,
		before: func(n *narrator, a narrArgs) string {
			return n.pick("Setter opp tabellen.", "Stiller opp radene.", "Rigger tabellen.")
		},
	},

	"web_search": {
		kind: kindWeb,
		before: func(n *narrator, a narrArgs) string {
			if q := strings.TrimSpace(a.Query); q != "" {
				return n.pick(
					"Søker på "+quote(q)+".",
					"Leter etter "+quote(q)+".",
					"Ser hva nettet vet om "+quote(q)+".",
					"Googler "+quote(q)+", i mangel av et bedre verb.",
				)
			}
			return n.pick("Søker.", "Leter på nettet.")
		},
		slow: []string{
			"Nettet er tregt akkurat nå. Leter videre.",
			"Fortsatt på jakt.",
			"Dette tar lengre tid enn det burde. Står på.",
		},
		after: func(n *narrator, r narrRes) string {
			if r.hits == 0 {
				return n.pick(
					"Ingenting brukbart der. Prøver en annen vinkel.",
					"Blankt. Internett skuffer i dag.",
					"Null treff. Formulerer meg om.",
				)
			}
			if r.hits == 1 {
				return n.pick("Én kilde. Det får holde.", "Bare én treffer. Leser den.")
			}
			return n.pick(
				fmt.Sprintf("%s kilder å gå gjennom.", nf(r.hits)),
				fmt.Sprintf("%s treff. Siler ut det som duger.", nf(r.hits)),
				fmt.Sprintf("Fant %s kilder. Leser.", nf(r.hits)),
			)
		},
	},

	"fetch_url": {
		kind: kindLink,
		before: func(n *narrator, a narrArgs) string {
			if h := host(a.URL); h != "" {
				return n.pick("Åpner "+h+".", "Tar en titt på "+h+".", "Leser "+h+".")
			}
			return n.pick("Åpner siden.", "Leser siden.")
		},
		slow: []string{
			"Siden bruker lang tid på å laste.",
			"Fortsatt lasting. Tålmodighet.",
			"Denne siden har tydeligvis mye på hjertet.",
		},
		after: func(n *narrator, r narrRes) string {
			if len([]rune(r.raw)) < 200 {
				return n.pick(
					"Tynn side. Lite å hente der.",
					"Mye layout, lite innhold.",
					"Nesten ingenting der. Videre.",
				)
			}
			return n.pick(
				"Lest. Plukker ut det som betyr noe.",
				"Gjennomlest. Siler ut poenget.",
				"Lest ferdig. Tar med meg det brukbare.",
			)
		},
	},

	"mail_search": {
		kind: kindMail,
		before: func(n *narrator, a narrArgs) string {
			if q := strings.TrimSpace(a.Query); q != "" {
				return n.pick(
					"Leter i innboksen etter "+quote(q)+".",
					"Graver i e-posten etter "+quote(q)+".",
					"Søker opp "+quote(q)+" i innboksen.",
				)
			}
			return n.pick("Leter i innboksen.", "Ser gjennom e-posten.")
		},
		slow: []string{
			"Innboksen tar sin tid.",
			"Fortsatt på leting i e-posten.",
			"Det er tydeligvis mye der inne.",
		},
		after: func(n *narrator, r narrRes) string {
			if r.hits == 0 {
				return n.pick(
					"Ingen e-post matcher. Den finnes kanskje under et annet navn.",
					"Ingenting i innboksen. Enten er den slettet, eller så het den noe helt annet.",
					"Null treff i e-posten.",
				)
			}
			return n.pick(
				fmt.Sprintf("%s e-poster å se på.", nf(r.hits)),
				fmt.Sprintf("%s treff i innboksen.", nf(r.hits)),
			)
		},
	},

	"mail_read": {
		kind: kindMail,
		before: func(n *narrator, a narrArgs) string {
			return n.pick("Leser e-posten.", "Åpner meldingen.", "Ser hva den sier.")
		},
		after: func(n *narrator, r narrRes) string {
			return n.pick("Lest. Ser hva som står der.", "Gjennomlest.", "Lest ferdig.")
		},
	},

	"m365_search": {
		kind: kindFile,
		before: func(n *narrator, a narrArgs) string {
			if q := strings.TrimSpace(a.Query); q != "" {
				return n.pick(
					"Leter i OneDrive etter "+quote(q)+".",
					"Søker opp "+quote(q)+" i filene dine.",
					"Ser om "+quote(q)+" ligger i OneDrive.",
				)
			}
			return n.pick("Leter i OneDrive.", "Ser gjennom filene dine.")
		},
		slow: []string{
			"OneDrive svarer sakte.",
			"Fortsatt på leting i filene.",
			"Microsoft tar seg god tid.",
		},
		after: func(n *narrator, r narrRes) string {
			if r.hits == 0 {
				return n.pick(
					"Ingen filer matcher søket.",
					"Ingenting i OneDrive. Den ligger nok et annet sted.",
					"Null treff blant filene.",
				)
			}
			return n.pick(
				fmt.Sprintf("%s filer å velge mellom.", nf(r.hits)),
				fmt.Sprintf("%s treff i OneDrive.", nf(r.hits)),
			)
		},
	},

	"m365_read": {
		kind: kindFile,
		before: func(n *narrator, a narrArgs) string {
			if name := strings.TrimSpace(a.Name); name != "" {
				return n.pick("Åpner "+name+".", "Leser "+name+".", "Tar en titt i "+name+".")
			}
			return n.pick("Åpner filen.", "Leser filen.")
		},
		slow: []string{
			"Stor fil. Leser fortsatt.",
			"Denne tar litt tid å komme gjennom.",
			"Fortsatt i filen. Den var lengre enn ventet.",
		},
		after: func(n *narrator, r narrRes) string {
			if len([]rune(r.raw)) < 200 {
				return n.pick(
					"Filen var nesten tom.",
					"Lite innhold der. Videre.",
					"Tom fil, i praksis.",
				)
			}
			return n.pick(
				"Lest gjennom. Henter ut det relevante.",
				"Gjennomlest. Plukker ut poengene.",
				"Ferdig lest. Tar med det som teller.",
			)
		},
	},

	"connect_database": {
		kind: kindDB,
		before: func(n *narrator, a narrArgs) string {
			return n.pick("Tester tilkoblingen.", "Ser om jeg kommer inn.", "Prøver å koble på.")
		},
		slow: []string{
			"Databasen svarer ikke ennå. Gir den noen sekunder til.",
			"Fortsatt ingen kontakt. Venter litt til.",
			"Stille i andre enden.",
		},
		after: func(n *narrator, r narrRes) string {
			return n.pick("Ser hva som ligger der inne.", "Inne. Kartlegger tabellene.", "Kontakt. Ser over skjemaet.")
		},
	},

	"list_agents": {
		kind: kindAgent,
		before: func(n *narrator, a narrArgs) string {
			return n.pick("Ser over rutinene dine.", "Henter rutinelista.", "Sjekker hvilke rutiner du har.")
		},
	},
	"create_agent": {kind: kindAgent, before: func(n *narrator, a narrArgs) string {
		return n.pick("Setter opp rutinen.", "Oppretter rutinen.")
	}},
	"update_agent": {kind: kindAgent, before: func(n *narrator, a narrArgs) string {
		return n.pick("Justerer rutinen.", "Endrer rutinen.")
	}},
	"delete_agent": {kind: kindAgent, before: func(n *narrator, a narrArgs) string {
		return n.pick("Fjerner rutinen.", "Sletter rutinen.")
	}},
	"setup_routine": {kind: kindAgent, before: func(n *narrator, a narrArgs) string {
		return n.pick("Setter opp rutinen.", "Rigger rutinen.")
	}},
	"save_m365_app": {kind: kindFile, before: func(n *narrator, a narrArgs) string {
		return n.pick("Lagrer app-registreringen.", "Tar vare på app-registreringen.")
	}},
	"contact_person": {
		kind: kindMail,
		before: func(n *narrator, a narrArgs) string {
			return n.pick("Setter opp e-posten.", "Skriver utkastet.", "Rigger e-posten.")
		},
	},
}

// --- tolkning av argumenter og resultater ---

func parseNarrArgs(raw string) narrArgs {
	var a narrArgs
	_ = json.Unmarshal([]byte(raw), &a)
	return a
}

// parseNarrRes tolker et verktøyresultat. Tabellsvar er JSON med columns/rows;
// alt annet måles grovt på lengde og linjer.
func parseNarrRes(result string) narrRes {
	r := narrRes{raw: result}
	trimmed := strings.TrimSpace(result)
	if strings.HasPrefix(trimmed, "{") {
		var qr struct {
			Columns  []string   `json:"columns"`
			Rows     [][]string `json:"rows"`
			RowCount *int       `json:"row_count"`
		}
		if json.Unmarshal([]byte(trimmed), &qr) == nil && qr.Columns != nil {
			r.ok = true
			r.cols = len(qr.Columns)
			r.rows = len(qr.Rows)
			if qr.RowCount != nil {
				r.rows = *qr.RowCount
			}
			r.hits = r.rows
			return r
		}
	}
	// Uten struktur: tell linjer som ser ut som listeoppføringer.
	for _, line := range strings.Split(trimmed, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") || numberedRe.MatchString(s) {
			r.hits++
		}
	}
	r.ok = trimmed != ""
	return r
}

var numberedRe = regexp.MustCompile(`^\d+[.)]\s`)

// describeSQL koker en SELECT ned til noen få ord brukeren kjenner igjen.
// Bevisst grov: står den fast, sier vi ingenting i stedet for noe feil.
func describeSQL(sql string) string {
	q := strings.TrimSpace(sql)
	if q == "" {
		return ""
	}
	lower := strings.ToLower(q)

	var table string
	if m := fromRe.FindStringSubmatch(lower); m != nil {
		table = strings.Trim(m[1], `"`)
		if i := strings.LastIndex(table, "."); i >= 0 {
			table = table[i+1:]
		}
	}
	if table == "" {
		return ""
	}

	// Aggregat slår rader: «hvor mange ordre» er mer presist enn «ordre».
	if aggRe.MatchString(lower) {
		if strings.Contains(lower, "group by") {
			return table + " gruppert"
		}
		return "et samletall fra " + table
	}
	if m := limitRe2.FindStringSubmatch(lower); m != nil && strings.Contains(lower, "order by") {
		return "de " + m[1] + " øverste i " + table
	}
	if strings.Contains(lower, "order by") {
		return table + " sortert"
	}
	return table
}

var (
	fromRe   = regexp.MustCompile(`\bfrom\s+([a-z0-9_."]+)`)
	aggRe    = regexp.MustCompile(`\b(count|sum|avg|min|max)\s*\(`)
	limitRe2 = regexp.MustCompile(`\blimit\s+(\d+)`)
)

// host trekker ut vertsnavnet i en URL, uten www.
func host(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(u.Host, "www.")
}

// quote setter søketeksten i anførselstegn, forkortet hvis den er lang.
func quote(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 48 {
		s = strings.TrimSpace(string(r[:48])) + "..."
	}
	return "\"" + s + "\""
}

// nf formaterer tall med mellomrom som tusenskille, norsk stil.
func nf(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 4 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}
