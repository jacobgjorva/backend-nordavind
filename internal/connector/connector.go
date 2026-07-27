// Package connector kobler Nordavind til kundens egne databaser:
// tilkobling, skjema-introspeksjon og streng read-only spørring.
package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
)

// Creds er tilkoblingsdetaljene kunden oppgir. Lagres kun kryptert.
type Creds struct {
	Driver   string `json:"driver"` // "postgres" | "mysql" | "mssql"
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// Column beskriver ett felt i en tabell.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Table er en introspektert tabell.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
	// Rows er et ESTIMAT fra databasens egen statistikk (gratis, ingen
	// skanning). Uten det skriver modellen fritekstsøk over millioner av
	// rader og spørringen timer ut — den må vite hva som er stort.
	Rows int64 `json:"rows,omitempty"`
}

// Link er en oppdaget eller manuelt definert join-nøkkel.
type Link struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
}

// queryTimeout: reelle kunde-spørringer (sortering over views uten indeks)
// trenger romsligere frist enn 10 s — modellen viser status underveis.
const queryTimeout = 30 * time.Second

// Open åpner en tilkobling og verifiserer den med ping.
func Open(ctx context.Context, c Creds) (*sql.DB, error) {
	var driver, dsn string
	switch c.Driver {
	case "postgres":
		driver = "pgx"
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=prefer&connect_timeout=5",
			url.QueryEscape(c.User), url.QueryEscape(c.Password), c.Host, c.Port, url.PathEscape(c.Database))
	case "mysql":
		driver = "mysql"
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=5s&readTimeout=15s",
			c.User, c.Password, c.Host, c.Port, c.Database)
	case "mssql":
		driver = "sqlserver"
		dsn = fmt.Sprintf("sqlserver://%s:%s@%s:%d?database=%s&dial+timeout=5",
			url.QueryEscape(c.User), url.QueryEscape(c.Password), c.Host, c.Port, url.QueryEscape(c.Database))
	default:
		return nil, fmt.Errorf("ukjent databasetype: %q", c.Driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Introspect leser tabeller, kolonner og fremmednøkler fra databasen.
// rowCounts henter tabellstørrelser fra databasens statistikk. Estimater er
// gode nok — poenget er å skille «tusen rader» fra «ti millioner».
func rowCounts(ctx context.Context, db *sql.DB, driver string) map[string]int64 {
	var q string
	switch driver {
	case "postgres":
		q = `SELECT relname, reltuples::bigint FROM pg_class c
		     JOIN pg_namespace n ON n.oid = c.relnamespace
		     WHERE n.nspname = 'public' AND c.relkind = 'r'`
	case "mysql":
		q = `SELECT table_name, table_rows FROM information_schema.tables
		     WHERE table_schema = DATABASE()`
	case "mssql":
		q = `SELECT t.name, SUM(p.rows) FROM sys.tables t
		     JOIN sys.partitions p ON p.object_id = t.object_id AND p.index_id IN (0,1)
		     GROUP BY t.name`
	default:
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil // statistikk er en bonus, aldri et krav
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n sql.NullInt64
		if err := rows.Scan(&name, &n); err == nil && n.Valid {
			out[strings.ToLower(name)] = n.Int64
		}
	}
	return out
}

func Introspect(ctx context.Context, db *sql.DB, c Creds) ([]Table, []Link, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var colsQ, fksQ string
	switch c.Driver {
	case "postgres":
		colsQ = `SELECT table_name, column_name, data_type
			 FROM information_schema.columns
			 WHERE table_schema = 'public' ORDER BY table_name, ordinal_position`
		fksQ = `SELECT kcu.table_name, kcu.column_name, ccu.table_name, ccu.column_name
			 FROM information_schema.table_constraints tc
			 JOIN information_schema.key_column_usage kcu ON tc.constraint_name = kcu.constraint_name
			 JOIN information_schema.constraint_column_usage ccu ON tc.constraint_name = ccu.constraint_name
			 WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = 'public'`
	case "mysql":
		colsQ = `SELECT table_name, column_name, data_type
			 FROM information_schema.columns
			 WHERE table_schema = DATABASE() ORDER BY table_name, ordinal_position`
		fksQ = `SELECT table_name, column_name, referenced_table_name, referenced_column_name
			 FROM information_schema.key_column_usage
			 WHERE table_schema = DATABASE() AND referenced_table_name IS NOT NULL`
	case "mssql":
		colsQ = `SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE
			 FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_SCHEMA = 'dbo' ORDER BY TABLE_NAME, ORDINAL_POSITION`
		fksQ = `SELECT kcu1.TABLE_NAME, kcu1.COLUMN_NAME, kcu2.TABLE_NAME, kcu2.COLUMN_NAME
			 FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS rc
			 JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu1
			   ON kcu1.CONSTRAINT_NAME = rc.CONSTRAINT_NAME
			 JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu2
			   ON kcu2.CONSTRAINT_NAME = rc.UNIQUE_CONSTRAINT_NAME
			  AND kcu2.ORDINAL_POSITION = kcu1.ORDINAL_POSITION`
	default:
		return nil, nil, fmt.Errorf("ukjent databasetype: %q", c.Driver)
	}

	rows, err := db.QueryContext(ctx, colsQ)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	byName := map[string]*Table{}
	var order []string
	for rows.Next() {
		var t, col, typ string
		if err := rows.Scan(&t, &col, &typ); err != nil {
			return nil, nil, err
		}
		tab, ok := byName[t]
		if !ok {
			tab = &Table{Name: t}
			byName[t] = tab
			order = append(order, t)
		}
		tab.Columns = append(tab.Columns, Column{Name: col, Type: typ})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var links []Link
	fkRows, err := db.QueryContext(ctx, fksQ)
	if err == nil {
		defer fkRows.Close()
		for fkRows.Next() {
			var l Link
			if err := fkRows.Scan(&l.FromTable, &l.FromColumn, &l.ToTable, &l.ToColumn); err == nil {
				links = append(links, l)
			}
		}
	}

	counts := rowCounts(ctx, db, c.Driver)
	tables := make([]Table, 0, len(order))
	for _, name := range order {
		t := *byName[name]
		t.Rows = counts[strings.ToLower(t.Name)]
		tables = append(tables, t)
	}
	return tables, links, nil
}

var (
	commentRe   = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)
	forbiddenRe = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|create|grant|revoke|truncate|attach|vacuum|copy|call|merge|execute|set|use|lock|pragma|into)\b`)
	tableRefRe  = regexp.MustCompile(`(?i)\b(?:from|join)\s+([a-zA-Z_][a-zA-Z0-9_."]*)`)
	limitRe     = regexp.MustCompile(`(?i)\blimit\s+\d+`)
	// Funksjoner der FROM er del av syntaksen (EXTRACT(YEAR FROM col),
	// SUBSTRING/TRIM/POSITION/OVERLAY) — ikke en tabellreferanse.
	fnFromRe = regexp.MustCompile(`(?i)\b(?:extract|substring|trim|position|overlay)\s*\([^()]*\)`)
)

// stripFnFrom fjerner funksjonskall som bruker FROM-syntaks, så tabell-
// uttrekket ikke forveksler en kolonne med et tabellnavn. Kjører til
// fikspunkt for enkelt nøstede kall.
func stripFnFrom(q string) string {
	for {
		next := fnFromRe.ReplaceAllString(q, " ")
		if next == q {
			return next
		}
		q = next
	}
}

const maxRows = 100

// ReferencedTables plukker ut tabellnavnene en spørring refererer til.
func ReferencedTables(query string) []string {
	var out []string
	for _, m := range tableRefRe.FindAllStringSubmatch(stripFnFrom(query), -1) {
		ref := strings.ToLower(strings.Trim(m[1], `"`))
		if i := strings.LastIndex(ref, "."); i >= 0 {
			ref = ref[i+1:]
		}
		out = append(out, ref)
	}
	return out
}

// cteNameRe fanger navnene i en WITH-klausul (WITH navn AS ( … )), inkludert
// påfølgende ledd etter komma.
var cteNameRe = regexp.MustCompile(`(?is)\b([a-z_][a-z0-9_]*)\s+as\s*\(`)

// CTENames er navnene en spørring definerer selv i WITH-klausuler. De er ikke
// tabeller i basen og skal derfor alltid regnes som tillatte referanser —
// uten dette avviser tilgangskontrollen enhver sammensatt spørring, og
// modellen mister det eneste verktøyet den har for flerstegs-beregninger
// («andel av de ti største kundene» feilet på nettopp dette).
func CTENames(query string) map[string]bool {
	names := map[string]bool{}
	for _, m := range cteNameRe.FindAllStringSubmatch(query, -1) {
		names[strings.ToLower(m[1])] = true
	}
	return names
}

// ExpandViews bytter FROM/JOIN-referanser til kuraterte utsnitt med
// utsnittets SQL som subquery. Modellen kan dermed bruke utsnittsnavnet uten
// at råtabellene bak trenger å være tillatt for fri spørring.
func ExpandViews(query string, views map[string]string) string {
	for name, sql := range views {
		re := regexp.MustCompile(`(?i)\b(from|join)\s+` + regexp.QuoteMeta(name) + `\b`)
		query = re.ReplaceAllString(query, "$1 ("+sql+") AS "+name)
	}
	return query
}

// SafeQueryViewsN er SafeQueryN med kuraterte utsnitt: modellens spørring
// valideres mot tillatte tabeller + utsnittsnavn, deretter ekspanderes
// utsnittene server-side. Råtabellene et utsnitt bruker er KUN kjørbare via
// ekspansjonen — aldri direkte fra modellens egen SQL.
func SafeQueryViewsN(ctx context.Context, db *sql.DB, driver, query string, allowedTables []string, views map[string]string, rowCap int) ([]string, [][]string, error) {
	visible := make(map[string]bool, len(allowedTables)+len(views))
	for _, t := range allowedTables {
		visible[strings.ToLower(t)] = true
	}
	for name := range views {
		visible[strings.ToLower(name)] = true
	}
	// Spørringens egne WITH-ledd er ikke tabeller i basen.
	for name := range CTENames(query) {
		visible[name] = true
	}
	for _, r := range ReferencedTables(query) {
		if !visible[r] {
			return nil, nil, fmt.Errorf("tabellen %q er ikke tilgjengelig", r)
		}
	}
	expanded := ExpandViews(query, views)
	all := append([]string{}, allowedTables...)
	for name, sql := range views {
		all = append(all, name)
		all = append(all, ReferencedTables(sql)...)
	}
	return SafeQueryN(ctx, db, driver, expanded, all, rowCap)
}

// SafeQuery kjører kun én SELECT-setning mot tillatte tabeller, med standard
// radgrense (100 — dimensjonert for LLM-kontekst).
func SafeQuery(ctx context.Context, db *sql.DB, driver, query string, allowed []string) ([]string, [][]string, error) {
	return SafeQueryN(ctx, db, driver, query, allowed, maxRows)
}

// SafeQueryN er SafeQuery med eksplisitt radgrense — widgets og eksport trenger
// langt flere rader enn LLM-konteksten (100 rader kuttet tidsserier til to
// måneder når dimensjonskolonner blåste opp radantallet).
// Radgrensen håndheves alltid ved lesing; LIMIT legges kun på for
// dialekter som støtter det (SQL Server bruker TOP og hopper over).
func SafeQueryN(ctx context.Context, db *sql.DB, driver, query string, allowed []string, rowCap int) ([]string, [][]string, error) {
	if rowCap <= 0 {
		rowCap = maxRows
	}
	q := strings.TrimSpace(commentRe.ReplaceAllString(query, " "))
	q = strings.TrimSuffix(q, ";")
	if strings.Contains(q, ";") {
		return nil, nil, errors.New("kun én setning er tillatt")
	}
	lower := strings.ToLower(q)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return nil, nil, errors.New("kun SELECT-spørringer er tillatt")
	}
	if forbiddenRe.MatchString(q) {
		return nil, nil, errors.New("spørringen inneholder ulovlige nøkkelord")
	}

	allowedSet := map[string]bool{}
	for _, t := range allowed {
		allowedSet[strings.ToLower(t)] = true
	}
	// CTE-navn regnes som tillatte referanser.
	for name := range CTENames(q) {
		allowedSet[name] = true
	}
	for _, m := range tableRefRe.FindAllStringSubmatch(stripFnFrom(q), -1) {
		ref := strings.ToLower(strings.Trim(m[1], `"`))
		if i := strings.LastIndex(ref, "."); i >= 0 {
			ref = ref[i+1:]
		}
		if !allowedSet[ref] {
			return nil, nil, fmt.Errorf("tabellen %q er ikke tilgjengelig", ref)
		}
	}

	if driver != "mssql" && !limitRe.MatchString(q) {
		q = fmt.Sprintf("%s LIMIT %d", q, rowCap)
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// Backstopp under regex-laget: kjør i en read-only transaksjon der
	// driveren støtter det, slik at en eventuell skrivende setning som
	// slipper forbi mønstervernet avvises på DB-nivå. Faller tilbake til
	// et vanlig kall for drivere som ikke støtter ReadOnly.
	var rows *sql.Rows
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		defer tx.Rollback()
		rows, err = tx.QueryContext(ctx, q)
	} else {
		// Driveren støtter ikke read-only-transaksjoner (f.eks. enkelte
		// SQL Server-oppsett): kjør vanlig kall — regex-vernet står fortsatt.
		rows, err = db.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]string
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() && len(out) < rowCap {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = cellString(v)
		}
		out = append(out, row)
	}
	return cols, out, rows.Err()
}

func cellString(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		s = string(x)
	case time.Time:
		s = x.Format("2006-01-02 15:04:05")
	case float64:
		// fmt.Sprint gir eksponentnotasjon på store tall — en omsetning på
		// 1,03 mrd ble vist som «1.0260736703026224e+09» i tabellen.
		// 'f' med -1 presisjon gir alle sifre uten å legge til falske
		// desimaler, så tallet forblir ordrett sporbart for kildekontrollen.
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(x), 'f', -1, 32)
	default:
		s = fmt.Sprint(x)
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}
