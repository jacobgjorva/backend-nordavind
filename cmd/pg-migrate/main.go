// pg-migrate kopierer hele Nordavind-databasen fra SQLite til Postgres
// (F1/F3 i docs/POSTGRES.md). Idempotent: hver kjøring tømmer måltabellene og
// kopierer på nytt, i én transaksjon per tabell. Verifiserer radantall.
//
//	go run ./cmd/pg-migrate -sqlite data/nordavind.db \
//	  -pg "postgres://nordavind:...@localhost:5432/nordavind"
//
// Bevisste oversettelser (jf. planen):
//   - INTEGER-flagg -> boolean, TIMESTAMP-tekst -> timestamptz i UTC,
//     BLOB -> bytea, embedding-JSON -> vector(4096) (tom/feil dim -> NULL).
//   - Døde kolonner porteres IKKE (agents.category, oppdrags-feltene).
//   - knowledge_notes.fts settes fra text+context (norsk config).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// tableSpec beskriver én tabell: kolonnene som porteres (i rekkefølge) og
// hvilke som trenger typekonvertering. Kolonner som ikke står her, porteres
// som de er (tekst/tall/bytea avgjøres av verdien fra SQLite).
type tableSpec struct {
	name    string
	cols    []string
	bools   map[string]bool
	times   map[string]bool
	vectors map[string]bool
	// identity: tabellen har generert id — sekvensen justeres etter kopiering.
	identity bool
	// ftsFrom: kolonner som bygger tsvector-kolonnen fts (kun knowledge_notes).
	ftsFrom []string
}

func set(cols ...string) map[string]bool {
	m := map[string]bool{}
	for _, c := range cols {
		m[c] = true
	}
	return m
}

// Rekkefølgen respekterer fremmednøklene (tenants før users osv.).
var tables = []tableSpec{
	{name: "tenants", cols: []string{"id", "name", "created_at"}, times: set("created_at")},
	{name: "users", cols: []string{"id", "tenant_id", "email", "role", "created_at"}, times: set("created_at")},
	{name: "sessions", cols: []string{"token", "user_id", "expires_at"}, times: set("expires_at")},
	{name: "login_codes", cols: []string{"email", "code", "expires_at", "used"}, times: set("expires_at"), bools: set("used")},
	{name: "usage_events", cols: []string{"id", "tenant_id", "user_id", "model", "prompt_tokens", "completion_tokens", "cost_usd", "searches", "duration_ms", "created_at"}, times: set("created_at"), identity: true},
	{name: "chats", cols: []string{"id", "tenant_id", "user_id", "title", "agent_id", "kind", "dashboard_spec", "folder_id", "created_at", "updated_at"}, times: set("created_at", "updated_at")},
	{name: "chat_messages", cols: []string{"id", "chat_id", "role", "content", "sources", "created_at"}, times: set("created_at"), identity: true},
	{name: "connections", cols: []string{"id", "tenant_id", "name", "driver", "creds_enc", "created_at"}, times: set("created_at")},
	{name: "connection_tables", cols: []string{"connection_id", "name", "description"}},
	{name: "connection_columns", cols: []string{"connection_id", "table_name", "column_name", "description", "col_type"}},
	{name: "connection_access", cols: []string{"connection_id", "table_name", "user_id"}},
	{name: "connection_links", cols: []string{"connection_id", "from_table", "from_column", "to_table", "to_column"}},
	{name: "connection_views", cols: []string{"connection_id", "name", "sql_text", "description"}},
	{name: "corrections", cols: []string{"id", "tenant_id", "user_id", "chat_id", "answer", "correction", "created_at"}, times: set("created_at"), identity: true},
	{name: "agents", cols: []string{"id", "tenant_id", "user_id", "name", "task", "connection_id", "schedule_label", "interval_seconds", "daily_token_limit", "write_access", "enabled", "run_time", "next_run_at", "last_run_at", "chat_id", "push_enabled", "mission_activity", "plan", "plan_status", "plan_error", "plan_built_at", "last_snapshot", "last_snapshot_at", "personality", "response_seen_at", "created_at"},
		times: set("next_run_at", "last_run_at", "plan_built_at", "last_snapshot_at", "response_seen_at", "created_at"),
		bools: set("write_access", "enabled", "push_enabled")},
	{name: "agent_runs", cols: []string{"id", "agent_id", "started_at", "status", "output", "tokens_used", "error", "alert"}, times: set("started_at"), bools: set("alert"), identity: true},
	{name: "knowledge_nodes", cols: []string{"id", "tenant_id", "type", "title", "summary", "status", "chat_id", "user_id", "embedding", "created_at"}, times: set("created_at"), vectors: set("embedding")},
	{name: "knowledge_edges", cols: []string{"tenant_id", "from_id", "to_id", "relation"}},
	{name: "widgets", cols: []string{"id", "tenant_id", "user_id", "slug", "title", "spec", "saved", "created_at", "updated_at"}, times: set("created_at", "updated_at"), bools: set("saved")},
	{name: "mail_accounts", cols: []string{"id", "user_id", "email", "imap_host", "imap_port", "smtp_host", "smtp_port", "pass_enc", "signature", "created_at", "updated_at"}, times: set("created_at", "updated_at")},
	{name: "document_chunks", cols: []string{"id", "node_id", "tenant_id", "ordinal", "content", "embedding", "created_at"}, times: set("created_at"), vectors: set("embedding")},
	{name: "documents", cols: []string{"node_id", "tenant_id", "filename", "raw_text", "title", "created_at"}, times: set("created_at")},
	{name: "knowledge_notes", cols: []string{"id", "tenant_id", "source_type", "source_id", "title", "text", "context", "status", "chat_id", "user_id", "embedding", "created_at"}, times: set("created_at"), vectors: set("embedding"), ftsFrom: []string{"text", "context"}},
	{name: "push_notifications", cols: []string{"id", "tenant_id", "user_id", "agent_id", "title", "body", "sent", "created_at"}, times: set("created_at"), bools: set("sent"), identity: true},
	{name: "employees", cols: []string{"id", "tenant_id", "name", "role", "description", "email", "created_at"}, times: set("created_at")},
	{name: "folders", cols: []string{"id", "tenant_id", "user_id", "name", "created_at"}, times: set("created_at")},
	{name: "export_links", cols: []string{"id", "tenant_id", "user_id", "title", "connection_id", "sql_text", "token_hash", "revoked", "created_at", "last_used_at"}, times: set("created_at", "last_used_at"), bools: set("revoked")},
	{name: "m365_accounts", cols: []string{"user_id", "tenant_id", "email", "refresh_enc", "created_at"}, times: set("created_at")},
	{name: "m365_apps", cols: []string{"tenant_id", "client_id", "secret_enc", "aad_tenant", "created_at"}, times: set("created_at")},
	{name: "onedrive_exports", cols: []string{"id", "tenant_id", "user_id", "title", "connection_id", "sql_text", "drive_item_id", "web_url", "created_at", "last_push_at"}, times: set("created_at", "last_push_at")},
	{name: "m365_states", cols: []string{"state", "tenant_id", "user_id", "expires_at"}, times: set("expires_at")},
	{name: "intent_decisions", cols: []string{"id", "tenant_id", "user_id", "message", "key", "method", "candidates", "elapsed_ms", "corrected", "created_at"}, times: set("created_at")},
}

// sqlite lagrer tid som tekst i flere varianter — alle tolkes som UTC
// (CURRENT_TIMESTAMP i SQLite ER UTC; Go-driveren skriver med sone).
var timeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999+07:00",
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999",
}

func parseTime(v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case time.Time:
		return t.UTC(), nil
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		for _, l := range timeLayouts {
			if ts, err := time.Parse(l, t); err == nil {
				return ts.UTC(), nil
			}
		}
		return nil, fmt.Errorf("ukjent tidsformat %q", t)
	case []byte:
		return parseTime(string(t))
	}
	return nil, fmt.Errorf("uventet tidstype %T", v)
}

func toBool(v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return t != 0, nil
	case bool:
		return t, nil
	}
	return nil, fmt.Errorf("uventet bool-type %T", v)
}

// toVector: JSON-array-tekst -> pgvector-literal. Tom/feil dimensjon -> NULL.
func toVector(v any, warn func(string)) (any, error) {
	var s string
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		return nil, fmt.Errorf("uventet embedding-type %T", v)
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var vec []float32
	if err := json.Unmarshal([]byte(s), &vec); err != nil {
		warn("uparsbar embedding hoppes over (NULL)")
		return nil, nil
	}
	if len(vec) != 4096 {
		warn(fmt.Sprintf("embedding med %d dims hoppes over (NULL)", len(vec)))
		return nil, nil
	}
	// pgvector aksepterer «[1,2,3]» — samme form som JSON-arrayen.
	b, _ := json.Marshal(vec)
	return string(b), nil
}

func main() {
	sqlitePath := flag.String("sqlite", "data/nordavind.db", "kilde (SQLite-fil)")
	pgURL := flag.String("pg", os.Getenv("DATABASE_URL"), "mål (postgres://…)")
	schema := flag.String("schema", "internal/store/pgschema.sql", "DDL som kjøres først")
	flag.Parse()
	if *pgURL == "" {
		fmt.Fprintln(os.Stderr, "mangler -pg / DATABASE_URL")
		os.Exit(2)
	}

	src, err := sql.Open("sqlite", *sqlitePath)
	fatal(err, "åpne sqlite")
	dst, err := sql.Open("pgx", *pgURL)
	fatal(err, "åpne postgres")
	ctx := context.Background()
	fatal(dst.PingContext(ctx), "koble til postgres")

	ddl, err := os.ReadFile(*schema)
	fatal(err, "lese skjema")
	_, err = dst.ExecContext(ctx, string(ddl))
	fatal(err, "kjøre skjema")

	failed := false
	for _, t := range tables {
		n, err := copyTable(ctx, src, dst, t)
		if err != nil {
			fmt.Printf("%-22s FEIL: %v\n", t.name, err)
			failed = true
			continue
		}
		var srcN int
		fatal(src.QueryRow("SELECT count(*) FROM "+t.name).Scan(&srcN), "telle "+t.name)
		mark := "ok"
		if n != srcN {
			mark = fmt.Sprintf("AVVIK (sqlite %d)", srcN)
			failed = true
		}
		fmt.Printf("%-22s %6d rader  %s\n", t.name, n, mark)
	}
	if failed {
		fmt.Println("\nMIGRERING RØD — se avvik over. Ingen delvis cut-over.")
		os.Exit(1)
	}
	fmt.Println("\nMigrering grønn: alle tabeller kopiert og verifisert.")
}

func copyTable(ctx context.Context, src, dst *sql.DB, t tableSpec) (int, error) {
	rows, err := src.Query("SELECT " + strings.Join(quoteAll(t.cols), ", ") + " FROM " + t.name)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Idempotens: tøm målet før kopiering. CASCADE fordi alt uansett fylles her.
	if _, err := tx.ExecContext(ctx, "TRUNCATE "+t.name+" CASCADE"); err != nil {
		return 0, err
	}

	insertCols := quoteAll(t.cols)
	ph := make([]string, len(t.cols))
	for i, c := range t.cols {
		ph[i] = fmt.Sprintf("$%d", i+1)
		if t.vectors[c] {
			ph[i] += "::vector"
		}
	}
	stmtSQL := "INSERT INTO " + t.name + " (" + strings.Join(insertCols, ", ") + ") VALUES (" + strings.Join(ph, ", ") + ")"
	if len(t.ftsFrom) > 0 {
		// fts bygges i samme insert: to_tsvector('norwegian', text+context).
		parts := make([]string, len(t.ftsFrom))
		for i, c := range t.ftsFrom {
			parts[i] = fmt.Sprintf("$%d", indexOf(t.cols, c)+1)
		}
		stmtSQL = strings.Replace(stmtSQL, ")", ", fts)", 1)
		stmtSQL = strings.Replace(stmtSQL,
			"VALUES ("+strings.Join(ph, ", ")+")",
			"VALUES ("+strings.Join(ph, ", ")+", to_tsvector('norwegian', "+strings.Join(parts, " || ' ' || ")+"))", 1)
	}
	stmt, err := tx.PrepareContext(ctx, stmtSQL)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	n := 0
	raw := make([]any, len(t.cols))
	ptrs := make([]any, len(t.cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		vals := make([]any, len(t.cols))
		for i, c := range t.cols {
			v := raw[i]
			var err error
			switch {
			case t.times != nil && t.times[c]:
				v, err = parseTime(v)
			case t.bools != nil && t.bools[c]:
				v, err = toBool(v)
			case t.vectors != nil && t.vectors[c]:
				v, err = toVector(v, func(msg string) { fmt.Printf("  %s.%s: %s\n", t.name, c, msg) })
			}
			if err != nil {
				return n, fmt.Errorf("rad %d, kolonne %s: %w", n+1, c, err)
			}
			vals[i] = v
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			return n, fmt.Errorf("rad %d: %w", n+1, err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if t.identity {
		// Sekvensen skal fortsette ETTER høyeste kopierte id.
		if _, err := tx.ExecContext(ctx,
			"SELECT setval(pg_get_serial_sequence('"+t.name+"','id'), COALESCE((SELECT max(id) FROM "+t.name+"), 1))"); err != nil {
			return n, err
		}
	}
	return n, tx.Commit()
}

func quoteAll(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = `"` + c + `"`
	}
	return out
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

func fatal(err error, what string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, what+":", err)
		os.Exit(1)
	}
}
