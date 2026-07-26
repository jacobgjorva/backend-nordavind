package store

import (
	"database/sql"
	_ "embed"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed pgschema.sql
var pgSchema string

// openPostgres kobler til Postgres og kjører det idempotente skjemaet
// (CREATE IF NOT EXISTS hele veien — trygt ved hver oppstart).
func openPostgres(url string) (*Store, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	// VPS-en er beskjeden: liten pool, aldri ubegrenset.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(pgSchema); err != nil {
		return nil, err
	}
	return &Store{db: &DB{sql: db, pg: true}}, nil
}
