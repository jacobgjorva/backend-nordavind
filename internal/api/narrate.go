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
	slow   string // vises hvis kallet varer lenger enn slowAfter
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
	if !ok || step.slow == "" {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(slowAfter):
			n.say(step.slow, step.kind)
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
var narration = map[string]narrStep{
	"query_database": {
		kind: kindDB,
		before: func(n *narrator, a narrArgs) string {
			base := "Spør databasen"
			if name := n.connName(a.ConnectionID); name != "" {
				base = "Spør " + name
			}
			if what := describeSQL(a.SQL); what != "" {
				return base + " om " + what + "."
			}
			return base + "."
		},
		slow: "Basen tenker fortsatt. Jeg venter.",
		after: func(n *narrator, r narrRes) string {
			if !r.ok {
				return "Basen svarte ikke som forventet. Prøver en annen vei."
			}
			switch r.rows {
			case 0:
				return "Null rader tilbake. Enten er det tomt der inne, eller så stilte jeg feil spørsmål."
			case 1:
				return "Én rad. Kort og greit."
			}
			return fmt.Sprintf("%s rader tilbake. Ser på tallene.", nf(r.rows))
		},
	},

	"show_table": {
		kind: kindTable,
		before: func(n *narrator, a narrArgs) string {
			return "Setter opp tabellen."
		},
	},

	"web_search": {
		kind: kindWeb,
		before: func(n *narrator, a narrArgs) string {
			if q := strings.TrimSpace(a.Query); q != "" {
				return n.pick("Søker på "+quote(q)+".", "Leter etter "+quote(q)+".")
			}
			return "Søker."
		},
		slow: "Nettet er tregt akkurat nå. Leter videre.",
		after: func(n *narrator, r narrRes) string {
			if r.hits == 0 {
				return "Ingenting brukbart der. Prøver en annen vinkel."
			}
			return fmt.Sprintf("%s kilder å gå gjennom.", nf(r.hits))
		},
	},

	"fetch_url": {
		kind: kindLink,
		before: func(n *narrator, a narrArgs) string {
			if h := host(a.URL); h != "" {
				return "Åpner " + h + "."
			}
			return "Åpner siden."
		},
		slow: "Siden bruker lang tid på å laste.",
		after: func(n *narrator, r narrRes) string {
			if len([]rune(r.raw)) < 200 {
				return "Tynn side. Lite å hente der."
			}
			return "Lest. Plukker ut det som betyr noe."
		},
	},

	"mail_search": {
		kind: kindMail,
		before: func(n *narrator, a narrArgs) string {
			if q := strings.TrimSpace(a.Query); q != "" {
				return "Leter i innboksen etter " + quote(q) + "."
			}
			return "Leter i innboksen."
		},
		slow: "Innboksen tar sin tid.",
		after: func(n *narrator, r narrRes) string {
			if r.hits == 0 {
				return "Ingen e-post matcher. Den finnes kanskje under et annet navn."
			}
			return fmt.Sprintf("%s e-poster å se på.", nf(r.hits))
		},
	},

	"mail_read": {
		kind:   kindMail,
		before: func(n *narrator, a narrArgs) string { return "Leser e-posten." },
		after: func(n *narrator, r narrRes) string {
			return "Lest. Ser hva som står der."
		},
	},

	"m365_search": {
		kind: kindFile,
		before: func(n *narrator, a narrArgs) string {
			if q := strings.TrimSpace(a.Query); q != "" {
				return "Leter i OneDrive etter " + quote(q) + "."
			}
			return "Leter i OneDrive."
		},
		slow: "OneDrive svarer sakte.",
		after: func(n *narrator, r narrRes) string {
			if r.hits == 0 {
				return "Ingen filer matcher søket."
			}
			return fmt.Sprintf("%s filer å velge mellom.", nf(r.hits))
		},
	},

	"m365_read": {
		kind: kindFile,
		before: func(n *narrator, a narrArgs) string {
			if name := strings.TrimSpace(a.Name); name != "" {
				return "Åpner " + name + "."
			}
			return "Åpner filen."
		},
		slow: "Stor fil. Leser fortsatt.",
		after: func(n *narrator, r narrRes) string {
			if len([]rune(r.raw)) < 200 {
				return "Filen var nesten tom."
			}
			return "Lest gjennom. Henter ut det relevante."
		},
	},

	"connect_database": {
		kind:   kindDB,
		before: func(n *narrator, a narrArgs) string { return "Tester tilkoblingen." },
		slow:   "Databasen svarer ikke ennå. Gir den noen sekunder til.",
		after: func(n *narrator, r narrRes) string {
			return "Ser hva som ligger der inne."
		},
	},

	"list_agents": {
		kind:   kindAgent,
		before: func(n *narrator, a narrArgs) string { return "Ser over rutinene dine." },
	},
	"create_agent":  {kind: kindAgent, before: func(n *narrator, a narrArgs) string { return "Setter opp rutinen." }},
	"update_agent":  {kind: kindAgent, before: func(n *narrator, a narrArgs) string { return "Justerer rutinen." }},
	"delete_agent":  {kind: kindAgent, before: func(n *narrator, a narrArgs) string { return "Fjerner rutinen." }},
	"setup_routine": {kind: kindAgent, before: func(n *narrator, a narrArgs) string { return "Setter opp rutinen." }},
	"save_m365_app": {kind: kindFile, before: func(n *narrator, a narrArgs) string { return "Lagrer app-registreringen." }},
	"contact_person": {
		kind:   kindMail,
		before: func(n *narrator, a narrArgs) string { return "Setter opp e-posten." },
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
