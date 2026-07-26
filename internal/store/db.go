package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// DB er det tynne dialektlaget over database/sql: all eksisterende kode
// skriver spørringer med `?`-plassholdere; mot Postgres skrives de om til
// `$n` her — ETT sted, aldri spredt utover datalaget. Alt annet (typer,
// upsert-syntaks med ON CONFLICT, TRUE/FALSE-literaler) er portabelt mellom
// SQLite og Postgres og håndteres i selve spørringene.
type DB struct {
	sql *sql.DB
	pg  bool
}

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.sql.Exec(d.rebind(query), args...)
}

func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.sql.Query(d.rebind(query), args...)
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.sql.QueryRow(d.rebind(query), args...)
}

func (d *DB) Begin() (*Tx, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx, pg: d.pg}, nil
}

// Tx speiler DB for transaksjoner — samme rebinding, samme kontrakt.
type Tx struct {
	tx *sql.Tx
	pg bool
}

func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.Exec(rebind(query, t.pg), args...)
}

func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.Query(rebind(query, t.pg), args...)
}

func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRow(rebind(query, t.pg), args...)
}

func (t *Tx) Commit() error   { return t.tx.Commit() }
func (t *Tx) Rollback() error { return t.tx.Rollback() }

func (d *DB) rebind(query string) string { return rebind(query, d.pg) }

func (d *DB) Close() error { return d.sql.Close() }

// rebind bytter `?` → `$1..$n` for Postgres. Hopper over innhold i
// enkelt/dobbelt-anførselstegn så en '?' i en streng-literal aldri røres.
func rebind(query string, pg bool) string {
	if !pg || !strings.ContainsRune(query, '?') {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	var quote rune
	for _, r := range query {
		if quote != 0 {
			b.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			b.WriteRune(r)
		case '?':
			n++
			b.WriteString("$")
			b.WriteString(phNum(n))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func phNum(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// Flag skanner boolske kolonner portabelt: SQLite gir int64 (0/1), Postgres
// gir bool — begge lander som Go-bool.
type Flag bool

func (f *Flag) Scan(v any) error {
	switch t := v.(type) {
	case nil:
		*f = false
	case bool:
		*f = Flag(t)
	case int64:
		*f = t != 0
	default:
		return fmt.Errorf("Flag: uventet type %T", v)
	}
	return nil
}
