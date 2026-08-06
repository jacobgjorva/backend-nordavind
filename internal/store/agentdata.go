package store

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Agent-datalager: hver agent kan eie et lite sett typede tabeller i appens
// egen Postgres, i et eget schema per agent. Skjemaet defineres i planen og
// valideres deterministisk her; all SQL mot lageret GENERERES fra skjemaet —
// fri SQL med skriverettigheter finnes ikke. Krever Postgres (prod); på
// SQLite avvises datastore-planer med en ærlig feilmelding.

type DataColumn struct {
	Name string `json:"name"`
	Type string `json:"type"` // text | number | date | bool
}

type DataTable struct {
	Name    string       `json:"name"`
	Columns []DataColumn `json:"columns"`
	Key     string       `json:"key,omitempty"` // kolonnen upsert nøkler på
}

const (
	maxDataTables  = 5
	maxDataColumns = 12
	maxDataRows    = 5000 // tak per tabell — dette er et notatlager, ikke et DW
)

var dataNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,29}$`)

var dataTypes = map[string]string{
	"text":   "TEXT",
	"number": "DOUBLE PRECISION",
	"date":   "DATE",
	"bool":   "BOOLEAN",
}

// reservedDataNames: kolliderer med genererte kolonner eller SQL-nøkkelord vi
// aldri vil se i identifikatorer.
var reservedDataNames = map[string]bool{
	"oppdatert": true, "select": true, "table": true, "user": true, "order": true,
	"group": true, "where": true, "from": true, "join": true,
}

// ValidateDataSchema sjekker et datastore-skjema deterministisk. Tom liste er
// lovlig (agenten har ikke noe lager).
func ValidateDataSchema(tables []DataTable) []string {
	var problems []string
	if len(tables) > maxDataTables {
		problems = append(problems, fmt.Sprintf("Datalageret kan ha maks %d tabeller.", maxDataTables))
	}
	seen := map[string]bool{}
	for _, t := range tables {
		if !dataNameRe.MatchString(t.Name) || reservedDataNames[t.Name] {
			problems = append(problems, fmt.Sprintf("Tabellnavnet %q er ugyldig — små bokstaver/tall/understrek, maks 30 tegn.", t.Name))
			continue
		}
		if seen[t.Name] {
			problems = append(problems, fmt.Sprintf("Tabellen %q er definert to ganger.", t.Name))
		}
		seen[t.Name] = true
		if len(t.Columns) == 0 || len(t.Columns) > maxDataColumns {
			problems = append(problems, fmt.Sprintf("Tabellen %q må ha 1-%d kolonner.", t.Name, maxDataColumns))
		}
		cols := map[string]bool{}
		for _, c := range t.Columns {
			if !dataNameRe.MatchString(c.Name) || reservedDataNames[c.Name] {
				problems = append(problems, fmt.Sprintf("Kolonnenavnet %q i %q er ugyldig.", c.Name, t.Name))
				continue
			}
			if cols[c.Name] {
				problems = append(problems, fmt.Sprintf("Kolonnen %q i %q er definert to ganger.", c.Name, t.Name))
			}
			cols[c.Name] = true
			if _, ok := dataTypes[c.Type]; !ok {
				problems = append(problems, fmt.Sprintf("Kolonnen %q i %q har ukjent type %q — bruk text, number, date eller bool.", c.Name, t.Name, c.Type))
			}
		}
		if t.Key != "" && !cols[t.Key] {
			problems = append(problems, fmt.Sprintf("Nøkkelen %q finnes ikke blant kolonnene i %q.", t.Key, t.Name))
		}
	}
	return problems
}

// agentDataSchema gir schemanavnet for en agent — kun trygge tegn fra id-en.
func agentDataSchema(agentID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(agentID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 16 {
			break
		}
	}
	return "agentdata_" + b.String()
}

// qi quoter en validert identifikator. Navnene har alt passert dataNameRe,
// men belt-og-bukser: doble anførselstegn i navnet dobles.
func qi(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func findDataTable(tables []DataTable, name string) *DataTable {
	for i := range tables {
		if tables[i].Name == name {
			return &tables[i]
		}
	}
	return nil
}

// createDataTableSQL genererer CREATE-setningen for én tabell.
func createDataTableSQL(schema string, t DataTable) string {
	var cols []string
	for _, c := range t.Columns {
		def := qi(c.Name) + " " + dataTypes[c.Type]
		if c.Name == t.Key {
			def += " PRIMARY KEY"
		}
		cols = append(cols, def)
	}
	cols = append(cols, `"oppdatert" TIMESTAMPTZ NOT NULL DEFAULT now()`)
	return "CREATE TABLE IF NOT EXISTS " + qi(schema) + "." + qi(t.Name) + " (" + strings.Join(cols, ", ") + ")"
}

// EnsureAgentData oppretter schema og tabeller for agentens datalager.
// Idempotent; eksisterende tabeller røres ikke (kolonneendringer er en
// fremtidig migreringsjobb, ikke noe som skjer i forbifarten).
func (s *Store) EnsureAgentData(agentID string, tables []DataTable) error {
	if len(tables) == 0 {
		return nil
	}
	if !s.db.pg {
		return fmt.Errorf("agent-datalager krever Postgres")
	}
	if problems := ValidateDataSchema(tables); len(problems) > 0 {
		return fmt.Errorf("ugyldig datastore-skjema: %s", strings.Join(problems, "; "))
	}
	schema := agentDataSchema(agentID)
	if _, err := s.db.Exec("CREATE SCHEMA IF NOT EXISTS " + qi(schema)); err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := s.db.Exec(createDataTableSQL(schema, t)); err != nil {
			return err
		}
	}
	return nil
}

// upsertDataSQL genererer INSERT/UPSERT for validerte verdier. Kolonnene
// sorteres så setningen er deterministisk (testbar og cache-vennlig).
func upsertDataSQL(schema string, t DataTable, values map[string]string) (string, []any, error) {
	if len(values) == 0 {
		return "", nil, fmt.Errorf("ingen verdier å lagre")
	}
	colType := map[string]string{}
	for _, c := range t.Columns {
		colType[c.Name] = c.Type
	}
	names := make([]string, 0, len(values))
	for k := range values {
		if _, ok := colType[k]; !ok {
			return "", nil, fmt.Errorf("kolonnen %q finnes ikke i %q", k, t.Name)
		}
		names = append(names, k)
	}
	sort.Strings(names)
	if t.Key != "" {
		hasKey := false
		for _, n := range names {
			hasKey = hasKey || n == t.Key
		}
		if !hasKey {
			return "", nil, fmt.Errorf("verdiene mangler nøkkelen %q", t.Key)
		}
	}
	var colSQL, ph []string
	var args []any
	for _, n := range names {
		colSQL = append(colSQL, qi(n))
		ph = append(ph, "?")
		args = append(args, values[n])
	}
	q := "INSERT INTO " + qi(schema) + "." + qi(t.Name) + " (" + strings.Join(colSQL, ", ") +
		") VALUES (" + strings.Join(ph, ", ") + ")"
	if t.Key != "" {
		var sets []string
		for _, n := range names {
			if n == t.Key {
				continue
			}
			sets = append(sets, qi(n)+" = EXCLUDED."+qi(n))
		}
		sets = append(sets, `"oppdatert" = now()`)
		q += " ON CONFLICT (" + qi(t.Key) + ") DO UPDATE SET " + strings.Join(sets, ", ")
	}
	return q, args, nil
}

// deleteDataSQL genererer DELETE med rene likhetsfiltre. Minst ett filter
// kreves — full tømming er en egen, eksplisitt handling vi ikke tilbyr ennå.
func deleteDataSQL(schema string, t DataTable, where map[string]string) (string, []any, error) {
	if len(where) == 0 {
		return "", nil, fmt.Errorf("sletting krever minst ett filter (kolonne=verdi)")
	}
	colType := map[string]string{}
	for _, c := range t.Columns {
		colType[c.Name] = c.Type
	}
	names := make([]string, 0, len(where))
	for k := range where {
		if _, ok := colType[k]; !ok {
			return "", nil, fmt.Errorf("kolonnen %q finnes ikke i %q", k, t.Name)
		}
		names = append(names, k)
	}
	sort.Strings(names)
	var conds []string
	var args []any
	for _, n := range names {
		conds = append(conds, qi(n)+" = ?")
		args = append(args, where[n])
	}
	q := "DELETE FROM " + qi(schema) + "." + qi(t.Name) + " WHERE " + strings.Join(conds, " AND ")
	return q, args, nil
}

// UpsertAgentRow lagrer én rad i agentens datalager, med radtak.
func (s *Store) UpsertAgentRow(agentID string, tables []DataTable, table string, values map[string]string) error {
	if !s.db.pg {
		return fmt.Errorf("agent-datalager krever Postgres")
	}
	t := findDataTable(tables, table)
	if t == nil {
		return fmt.Errorf("tabellen %q finnes ikke i agentens datalager", table)
	}
	schema := agentDataSchema(agentID)
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + qi(schema) + "." + qi(t.Name)).Scan(&n); err == nil && n >= maxDataRows {
		return fmt.Errorf("tabellen %q er full (%d rader)", table, maxDataRows)
	}
	q, args, err := upsertDataSQL(schema, *t, values)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(q, args...)
	return err
}

// DeleteAgentRows sletter rader etter likhetsfiltre og returnerer antallet.
func (s *Store) DeleteAgentRows(agentID string, tables []DataTable, table string, where map[string]string) (int64, error) {
	if !s.db.pg {
		return 0, fmt.Errorf("agent-datalager krever Postgres")
	}
	t := findDataTable(tables, table)
	if t == nil {
		return 0, fmt.Errorf("tabellen %q finnes ikke i agentens datalager", table)
	}
	q, args, err := deleteDataSQL(agentDataSchema(agentID), *t, where)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ReadAgentTable leser hele tabellen (til plan-steg, chat og rapporter) i
// samme columns/rows-format som databaseverktøyet bruker.
func (s *Store) ReadAgentTable(agentID string, tables []DataTable, table string) ([]string, [][]string, error) {
	if !s.db.pg {
		return nil, nil, fmt.Errorf("agent-datalager krever Postgres")
	}
	t := findDataTable(tables, table)
	if t == nil {
		return nil, nil, fmt.Errorf("tabellen %q finnes ikke i agentens datalager", table)
	}
	var cols []string
	for _, c := range t.Columns {
		cols = append(cols, qi(c.Name))
	}
	order := qi(t.Columns[0].Name)
	if t.Key != "" {
		order = qi(t.Key)
	}
	q := "SELECT " + strings.Join(cols, ", ") + " FROM " + agentDataSchemaQualified(agentID, t.Name) +
		" ORDER BY " + order + " LIMIT 500"
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	var out [][]string
	vals := make([]any, len(names))
	ptrs := make([]any, len(names))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(names))
		for i, v := range vals {
			row[i] = dataCell(v)
		}
		out = append(out, row)
	}
	return names, out, rows.Err()
}

func agentDataSchemaQualified(agentID, table string) string {
	return qi(agentDataSchema(agentID)) + "." + qi(table)
}

// dataCell gjør en skannet verdi lesbar — samme ånd som connector.cellString.
func dataCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSuffix(fmt.Sprintf("%v", t), " +0000 UTC")
	}
}
