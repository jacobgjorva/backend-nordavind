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
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Creds er tilkoblingsdetaljene kunden oppgir. Lagres kun kryptert.
type Creds struct {
	Driver   string `json:"driver"` // "postgres" | "mysql"
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
}

// Link er en oppdaget eller manuelt definert join-nøkkel.
type Link struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
}

const queryTimeout = 10 * time.Second

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

	tables := make([]Table, 0, len(order))
	for _, name := range order {
		tables = append(tables, *byName[name])
	}
	return tables, links, nil
}

var (
	commentRe   = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)
	forbiddenRe = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|create|grant|revoke|truncate|attach|vacuum|copy|call|merge|execute|set|use|lock|pragma|into)\b`)
	tableRefRe  = regexp.MustCompile(`(?i)\b(?:from|join)\s+([a-zA-Z_][a-zA-Z0-9_."]*)`)
	limitRe     = regexp.MustCompile(`(?i)\blimit\s+\d+`)
)

const maxRows = 100

// SafeQuery kjører kun én SELECT-setning mot tillatte tabeller.
func SafeQuery(ctx context.Context, db *sql.DB, query string, allowed []string) ([]string, [][]string, error) {
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
	for _, m := range regexp.MustCompile(`(?i)\b([a-zA-Z_][a-zA-Z0-9_]*)\s+as\s*\(`).FindAllStringSubmatch(q, -1) {
		allowedSet[strings.ToLower(m[1])] = true
	}
	for _, m := range tableRefRe.FindAllStringSubmatch(q, -1) {
		ref := strings.ToLower(strings.Trim(m[1], `"`))
		if i := strings.LastIndex(ref, "."); i >= 0 {
			ref = ref[i+1:]
		}
		if !allowedSet[ref] {
			return nil, nil, fmt.Errorf("tabellen %q er ikke tilgjengelig", ref)
		}
	}

	if !limitRe.MatchString(q) {
		q = fmt.Sprintf("%s LIMIT %d", q, maxRows)
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, q)
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
	for rows.Next() && len(out) < maxRows {
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
	default:
		s = fmt.Sprint(x)
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}
