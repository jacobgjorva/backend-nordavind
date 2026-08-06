package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Vaktbikkje: varselregler som evalueres i KODE, ikke av modellen. Modellens
// alert-bool i report var ikke-deterministisk — samme tall kunne gi varsel den
// ene kjøringen og stillhet den neste. En watchRule leser et sql-stegs rådata,
// aggregerer én kolonne og sammenligner med terskelen; resultatet er det samme
// hver gang, og «Funn»-linja skrives deterministisk med målt verdi mot regel.

type watchRule struct {
	Step   int     `json:"step"`             // 1-basert: sql-steget regelen leser
	Column string  `json:"column,omitempty"` // kolonnen som måles (valgfri for agg=count)
	Agg    string  `json:"agg,omitempty"`    // last|first|sum|avg|min|max|count — tom = last
	Op     string  `json:"op"`               // > >= < <= = !=
	Value  float64 `json:"value"`
	Pct    bool    `json:"pct,omitempty"`    // regelen gjelder %-endring siden forrige kjøring
	Label  string  `json:"label,omitempty"`  // hva målingen heter i Funn-linja
}

var watchOps = map[string]func(a, b float64) bool{
	">":  func(a, b float64) bool { return a > b },
	">=": func(a, b float64) bool { return a >= b },
	"<":  func(a, b float64) bool { return a < b },
	"<=": func(a, b float64) bool { return a <= b },
	"=":  func(a, b float64) bool { return a == b },
	"==": func(a, b float64) bool { return a == b },
	"!=": func(a, b float64) bool { return a != b },
}

var watchAggs = map[string]bool{
	"": true, "last": true, "first": true, "sum": true,
	"avg": true, "min": true, "max": true, "count": true,
}

// stepTable leser et sql-stegs rådata (columns/rows-JSON).
func stepTable(raw string) ([]string, [][]string, bool) {
	var res struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil || len(res.Columns) == 0 {
		return nil, nil, false
	}
	return res.Columns, res.Rows, true
}

// ruleMeasure aggregerer regelens kolonne i ett stegs rådata. ok=false når
// målingen ikke lar seg gjøre (ukjent kolonne, ingen tall) — en regel som ikke
// kan måles skal aldri utløse varsel, men heller ikke skjule at den er blind.
func ruleMeasure(r watchRule, raw string) (float64, bool) {
	cols, rows, ok := stepTable(raw)
	if !ok {
		return 0, false
	}
	if strings.EqualFold(strings.TrimSpace(r.Agg), "count") && strings.TrimSpace(r.Column) == "" {
		return float64(len(rows)), true
	}
	idx := -1
	for i, c := range cols {
		if strings.EqualFold(strings.TrimSpace(c), strings.TrimSpace(r.Column)) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, false
	}
	var vals []float64
	for _, row := range rows {
		if idx >= len(row) {
			continue
		}
		if v, ok := parseLooseNumber(row[idx]); ok {
			vals = append(vals, v)
		}
	}
	agg := strings.ToLower(strings.TrimSpace(r.Agg))
	if agg == "count" {
		return float64(len(vals)), true
	}
	if len(vals) == 0 {
		return 0, false
	}
	switch agg {
	case "", "last":
		return vals[len(vals)-1], true
	case "first":
		return vals[0], true
	case "sum", "avg":
		var sum float64
		for _, v := range vals {
			sum += v
		}
		if agg == "avg" {
			return sum / float64(len(vals)), true
		}
		return sum, true
	case "min":
		m := vals[0]
		for _, v := range vals[1:] {
			if v < m {
				m = v
			}
		}
		return m, true
	case "max":
		m := vals[0]
		for _, v := range vals[1:] {
			if v > m {
				m = v
			}
		}
		return m, true
	}
	return 0, false
}

// snapshotStepRaw henter et stegs rådata fra forrige kjørings snapshot.
func snapshotStepRaw(snapshot string, step int) (string, bool) {
	var snap []map[string]string
	if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
		return "", false
	}
	if step < 1 || step > len(snap) {
		return "", false
	}
	return snap[step-1]["data"], true
}

// formatMeasure skriver tall slik rapportene ellers gjør: heltall med
// mellomrom som tusenskille, ellers to desimaler med komma.
func formatMeasure(v float64) string {
	if v == float64(int64(v)) {
		s := strconv.FormatInt(int64(v), 10)
		neg := strings.HasPrefix(s, "-")
		s = strings.TrimPrefix(s, "-")
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + " " + s[i:]
		}
		if neg {
			s = "-" + s
		}
		return s
	}
	return strings.ReplaceAll(strconv.FormatFloat(v, 'f', 2, 64), ".", ",")
}

// evaluateWatchRules avgjør varselet deterministisk: minst én oppfylt regel =
// varsel. Returnerer også Funn-linjene (målt verdi mot regel) og evt. regler
// som ikke lot seg måle denne kjøringen.
func evaluateWatchRules(rules []watchRule, steps []stepResult, lastSnapshot string) (alert bool, findings, blind []string) {
	for _, r := range rules {
		cmp, ok := watchOps[strings.TrimSpace(r.Op)]
		if !ok || r.Step < 1 || r.Step > len(steps) {
			blind = append(blind, ruleName(r))
			continue
		}
		val, measured := ruleMeasure(r, steps[r.Step-1].raw)
		if !measured {
			blind = append(blind, ruleName(r))
			continue
		}
		unit := ""
		if r.Pct {
			prevRaw, has := snapshotStepRaw(lastSnapshot, r.Step)
			if !has {
				// Første kjøring: ingen forrige verdi å regne endring mot.
				continue
			}
			prev, ok := ruleMeasure(r, prevRaw)
			if !ok || prev == 0 {
				blind = append(blind, ruleName(r))
				continue
			}
			val = (val - prev) / prev * 100
			unit = " %"
		}
		if cmp(val, r.Value) {
			alert = true
			findings = append(findings, fmt.Sprintf("%s: %s%s (regel: %s %s%s)",
				ruleName(r), formatMeasure(val), unit, r.Op, formatMeasure(r.Value), unit))
		}
	}
	return alert, findings, blind
}

func ruleName(r watchRule) string {
	if strings.TrimSpace(r.Label) != "" {
		return strings.TrimSpace(r.Label)
	}
	if strings.TrimSpace(r.Column) != "" {
		return strings.TrimSpace(r.Column)
	}
	return fmt.Sprintf("steg %d", r.Step)
}

// funnBlock er den deterministiske varsellinja øverst i rapporten — skrevet av
// koden fra målte verdier, aldri av modellen.
func funnBlock(findings []string) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**Funn:**\n")
	for _, f := range findings {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// validateWatchRules sjekker reglene mot prøvekjøringens faktiske resultater,
// så en regel mot en kolonne som ikke finnes avvises ved spinup — ikke ved
// første kjøring midt på natten.
func validateWatchRules(rules []watchRule, plan agentPlan, trial map[int]string) []string {
	var problems []string
	for i, r := range rules {
		n := i + 1
		if _, ok := watchOps[strings.TrimSpace(r.Op)]; !ok {
			problems = append(problems, fmt.Sprintf("Varselregel %d har ukjent op %q — bruk >, >=, <, <=, = eller !=.", n, r.Op))
		}
		if !watchAggs[strings.ToLower(strings.TrimSpace(r.Agg))] {
			problems = append(problems, fmt.Sprintf("Varselregel %d har ukjent agg %q — bruk last, first, sum, avg, min, max eller count.", n, r.Agg))
		}
		if r.Step < 1 || r.Step > len(plan.Steps) {
			problems = append(problems, fmt.Sprintf("Varselregel %d peker på steg %d som ikke finnes.", n, r.Step))
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(plan.Steps[r.Step-1].Kind))
		if kind == "data" {
			// Datalager-steg: tabellen er nyopprettet og tom ved spinup, så
			// målingen kan ikke prøvekjøres — men kolonnen må finnes i skjemaet.
			t := findPlanDataTable(plan, plan.Steps[r.Step-1].Table)
			colOK := strings.EqualFold(strings.TrimSpace(r.Agg), "count") && strings.TrimSpace(r.Column) == ""
			if t != nil {
				for _, c := range t.Columns {
					colOK = colOK || strings.EqualFold(c.Name, strings.TrimSpace(r.Column))
				}
			}
			if !colOK {
				problems = append(problems, fmt.Sprintf("Varselregel %d: kolonnen %q finnes ikke i datalager-tabellen for steg %d.", n, r.Column, r.Step))
			}
			continue
		}
		if kind != "sql" {
			problems = append(problems, fmt.Sprintf("Varselregel %d peker på steg %d, men bare sql- og data-steg kan måles.", n, r.Step))
			continue
		}
		raw, ok := trial[r.Step]
		if !ok {
			continue // steget feilet ved prøvekjøring — det meldes separat
		}
		if _, measured := ruleMeasure(r, raw); !measured {
			cols, _, _ := stepTable(raw)
			problems = append(problems, fmt.Sprintf("Varselregel %d: kolonnen %q ga ingen målbare tall i steg %d. Kolonner: %s",
				n, r.Column, r.Step, strings.Join(cols, ", ")))
		}
	}
	return problems
}

// gateAgentSummary kjører kildekontrollen på rapport-prosaen fra en headless
// agent-kjøring. Rene tallavvik slipper gjennom (delta og snitt er lovlige
// beregninger — tabellen står ved siden av som kvittering); diktede navn gjør
// at prosaen holdes tilbake. Returnerer ok=false når prosaen ikke kan vises.
func gateAgentSummary(summary string, basis []string) bool {
	off := groundingOffenders(summary, basis)
	return len(off) == 0 || !hasNameOffender(off)
}
