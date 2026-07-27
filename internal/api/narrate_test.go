package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// capture samler stegene en narrator sender.
func capture(on bool) (*narrator, *[]string) {
	var steps []string
	n := newNarrator(func(line string) {
		var m struct {
			Step string `json:"nordavind_step"`
		}
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m)
		if m.Step != "" {
			steps = append(steps, m.Step)
		}
	}, on, func(id string) string {
		if id == "conn1" {
			return "Kunder"
		}
		return ""
	})
	return n, &steps
}

func TestNarratorDatabaseFlow(t *testing.T) {
	n, steps := capture(true)
	args := `{"connection_id":"conn1","sql":"SELECT c.customer_name, COUNT(o.id) FROM customers c JOIN orders o ON c.id = o.customer_id GROUP BY c.customer_name ORDER BY 2 DESC LIMIT 5"}`
	n.before("query_database", args)
	n.after("query_database", `{"columns":["customer_name","n"],"rows":[["a","1"],["b","2"]],"row_count":2}`)

	if len(*steps) != 2 {
		t.Fatalf("ventet 2 steg, fikk %d: %v", len(*steps), *steps)
	}
	if !strings.Contains((*steps)[0], "Kunder") {
		t.Errorf("før-steget skal navngi tilkoblingen: %q", (*steps)[0])
	}
	if !strings.Contains((*steps)[1], "2 rader") {
		t.Errorf("etter-steget skal si hva som faktisk kom: %q", (*steps)[1])
	}
}

func TestNarratorEmptyResultIsHonest(t *testing.T) {
	n, steps := capture(true)
	n.after("query_database", `{"columns":["a"],"rows":[],"row_count":0}`)
	if len(*steps) != 1 || !strings.Contains((*steps)[0], "Null rader") {
		t.Fatalf("tomt svar skal sies rett ut: %v", *steps)
	}
}

// Utfører-flytene (widget, deck, agent-oppsett) skal kun få den korte
// kvitteringen — aldri funn- eller ventemeldinger.
func TestNarratorOffOutsidePlainChat(t *testing.T) {
	n, steps := capture(false)
	n.before("query_database", `{"connection_id":"conn1","sql":"SELECT * FROM orders"}`)
	n.after("query_database", `{"columns":["a"],"rows":[],"row_count":0}`)
	n.afterHits("web_search", 0)

	if len(*steps) != 1 {
		t.Fatalf("ventet kun før-steget, fikk %v", *steps)
	}
	if !strings.Contains((*steps)[0], "Spør Kunder") {
		t.Errorf("uventet før-steg: %q", (*steps)[0])
	}
}

func TestNarratorNoRepeats(t *testing.T) {
	n, steps := capture(true)
	n.say("Setter opp tabellen.", kindTable)
	n.say("Setter opp tabellen.", kindTable)
	if len(*steps) != 1 {
		t.Fatalf("identisk tekst to ganger skal svelges: %v", *steps)
	}
}

// Samme verktøy to ganger på rad skal ikke lese likt — variantene roterer.
func TestNarratorVariesPhrasing(t *testing.T) {
	n, steps := capture(true)
	n.before("show_table", `{}`)
	n.before("show_table", `{}`)
	if len(*steps) != 2 {
		t.Fatalf("ventet to ulike steg, fikk %v", *steps)
	}
	if (*steps)[0] == (*steps)[1] {
		t.Errorf("gjentatt kall ga samme ordlyd: %q", (*steps)[0])
	}
}

// Hver variant i registeret skal være en hel setning — ingen halve strenger
// eller manglende punktum som avslører at en variant er slurvet inn.
func TestNarrationVariantsAreWellFormed(t *testing.T) {
	check := func(tool, phase, s string) {
		if strings.TrimSpace(s) == "" {
			t.Errorf("%s/%s: tom tekst", tool, phase)
			return
		}
		if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") {
			t.Errorf("%s/%s: mangler punktum: %q", tool, phase, s)
		}
		if r := []rune(s); r[0] != []rune(strings.ToUpper(string(r[0])))[0] {
			t.Errorf("%s/%s: starter ikke med stor bokstav: %q", tool, phase, s)
		}
	}
	for tool, step := range narration {
		for _, slow := range step.slow {
			check(tool, "slow", slow)
		}
		// Roter gjennom nok steg til at alle varianter er innom.
		for i := 0; i < 12; i++ {
			n, _ := capture(true)
			n.turn = i
			if step.before != nil {
				check(tool, "before", step.before(n, narrArgs{Query: "test", URL: "https://x.no/a", Name: "fil.xlsx"}))
			}
			if step.after != nil {
				check(tool, "after", step.after(n, narrRes{ok: true, rows: i * 400, hits: i}))
			}
		}
	}
}

func TestDescribeSQL(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM orders":                                 "orders",
		"SELECT * FROM public.orders ORDER BY id DESC LIMIT 5": "de 5 øverste i orders",
		"SELECT COUNT(*) FROM customers":                       "et samletall fra customers",
		"SELECT name, SUM(x) FROM sales GROUP BY name":         "sales gruppert",
		"SELECT * FROM products ORDER BY price":                "products sortert",
		"":                                                     "",
		"WITH t AS (SELECT 1) SELECT * FROM t":                 "t",
	}
	for sql, want := range cases {
		if got := describeSQL(sql); got != want {
			t.Errorf("describeSQL(%q) = %q, ventet %q", sql, got, want)
		}
	}
}

func TestNF(t *testing.T) {
	for in, want := range map[int]string{5: "5", 775: "775", 1234: "1234", 17682: "17 682", 1234567: "1 234 567"} {
		if got := nf(in); got != want {
			t.Errorf("nf(%d) = %q, ventet %q", in, got, want)
		}
	}
}

// Registeret skal aldri sende tom eller uferdig tekst.
func TestNarrationEntriesProduceText(t *testing.T) {
	for tool, step := range narration {
		if step.before != nil {
			n, _ := capture(true)
			if s := step.before(n, narrArgs{}); strings.TrimSpace(s) == "" {
				t.Errorf("%s: before gir tom tekst uten argumenter", tool)
			}
		}
		if step.after != nil {
			n, _ := capture(true)
			if s := step.after(n, narrRes{}); strings.TrimSpace(s) == "" {
				t.Errorf("%s: after gir tom tekst uten resultat", tool)
			}
		}
	}
}
