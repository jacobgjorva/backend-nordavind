package store

import (
	"database/sql"
	"errors"
)

// Connection er en kundedatabase-tilkobling (kredensialer lagres kryptert).
type Connection struct {
	ID       string `json:"id"`
	TenantID string `json:"-"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
}

// TableConfig er admin-kuratert konfigurasjon for én tabell.
type TableConfig struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Columns     map[string]string `json:"columns"`  // kolonnenavn -> beskrivelse (valgfri)
	UserIDs     []string          `json:"user_ids"` // tom = alle brukere
	// Full kolonneliste (navn + type) lagret ved siste introspeksjon —
	// dette er skjemaet AI-en får se.
	ColumnList []ColumnInfo `json:"-"`
}

// ColumnInfo er navn/type/beskrivelse for ett felt, slik AI-en ser det.
type ColumnInfo struct {
	Name        string
	Type        string
	Description string
}

// LinkConfig er en join-nøkkel mellom to tabeller.
type LinkConfig struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
}

func (s *Store) migrateConnections() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS connections (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL REFERENCES tenants(id),
			name       TEXT NOT NULL,
			driver     TEXT NOT NULL,
			creds_enc  BLOB NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS connection_tables (
			connection_id TEXT NOT NULL REFERENCES connections(id),
			name          TEXT NOT NULL,
			description   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (connection_id, name)
		);
		CREATE TABLE IF NOT EXISTS connection_columns (
			connection_id TEXT NOT NULL,
			table_name    TEXT NOT NULL,
			column_name   TEXT NOT NULL,
			col_type      TEXT NOT NULL DEFAULT '',
			description   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (connection_id, table_name, column_name)
		);
		CREATE TABLE IF NOT EXISTS connection_access (
			connection_id TEXT NOT NULL,
			table_name    TEXT NOT NULL,
			user_id       TEXT NOT NULL,
			PRIMARY KEY (connection_id, table_name, user_id)
		);
		CREATE TABLE IF NOT EXISTS connection_links (
			connection_id TEXT NOT NULL,
			from_table    TEXT NOT NULL,
			from_column   TEXT NOT NULL,
			to_table      TEXT NOT NULL,
			to_column     TEXT NOT NULL,
			PRIMARY KEY (connection_id, from_table, from_column, to_table, to_column)
		);
	`)
	if err != nil {
		return err
	}
	// Eldre dev-databaser mangler col_type — legg til om nødvendig.
	s.db.Exec(`ALTER TABLE connection_columns ADD COLUMN col_type TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *Store) CreateConnection(tenantID, name, driver string, credsEnc []byte) (Connection, error) {
	c := Connection{ID: newID(), TenantID: tenantID, Name: name, Driver: driver}
	_, err := s.db.Exec(
		`INSERT INTO connections (id, tenant_id, name, driver, creds_enc) VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.TenantID, c.Name, c.Driver, credsEnc,
	)
	return c, err
}

func (s *Store) ListConnections(tenantID string) ([]Connection, error) {
	rows, err := s.db.Query(
		`SELECT id, name, driver FROM connections WHERE tenant_id = ? ORDER BY created_at`, tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c := Connection{TenantID: tenantID}
		if err := rows.Scan(&c.ID, &c.Name, &c.Driver); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConnectionCreds returnerer krypterte kredensialer, med eierskapssjekk.
func (s *Store) ConnectionCreds(tenantID, id string) (Connection, []byte, error) {
	c := Connection{ID: id, TenantID: tenantID}
	var enc []byte
	err := s.db.QueryRow(
		`SELECT name, driver, creds_enc FROM connections WHERE id = ? AND tenant_id = ?`, id, tenantID,
	).Scan(&c.Name, &c.Driver, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil, ErrNotFound
	}
	return c, enc, err
}

func (s *Store) DeleteConnection(tenantID, id string) error {
	res, err := s.db.Exec(`DELETE FROM connections WHERE id = ? AND tenant_id = ?`, id, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, t := range []string{"connection_tables", "connection_columns", "connection_access", "connection_links"} {
		if _, err := s.db.Exec(`DELETE FROM `+t+` WHERE connection_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// SaveConnectionConfig erstatter hele kurateringen for en tilkobling.
// colTypes er live-introspekterte kolonner per tabell (navn -> type) —
// alle lagres slik at AI-en ser hele skjemaet, ikke bare beskrevne felt.
func (s *Store) SaveConnectionConfig(connID string, tables []TableConfig, links []LinkConfig, colTypes map[string]map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, t := range []string{"connection_tables", "connection_columns", "connection_access", "connection_links"} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE connection_id = ?`, connID); err != nil {
			return err
		}
	}
	for _, t := range tables {
		if _, err := tx.Exec(
			`INSERT INTO connection_tables (connection_id, name, description) VALUES (?, ?, ?)`,
			connID, t.Name, t.Description,
		); err != nil {
			return err
		}
		for col, typ := range colTypes[t.Name] {
			if _, err := tx.Exec(
				`INSERT INTO connection_columns (connection_id, table_name, column_name, col_type, description) VALUES (?, ?, ?, ?, ?)`,
				connID, t.Name, col, typ, t.Columns[col],
			); err != nil {
				return err
			}
		}
		for _, uid := range t.UserIDs {
			if _, err := tx.Exec(
				`INSERT INTO connection_access (connection_id, table_name, user_id) VALUES (?, ?, ?)`,
				connID, t.Name, uid,
			); err != nil {
				return err
			}
		}
	}
	for _, l := range links {
		if _, err := tx.Exec(
			`INSERT INTO connection_links (connection_id, from_table, from_column, to_table, to_column) VALUES (?, ?, ?, ?, ?)`,
			connID, l.FromTable, l.FromColumn, l.ToTable, l.ToColumn,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ConnectionConfig returnerer lagret kuratering for en tilkobling.
func (s *Store) ConnectionConfig(connID string) ([]TableConfig, []LinkConfig, error) {
	rows, err := s.db.Query(
		`SELECT name, description FROM connection_tables WHERE connection_id = ? ORDER BY name`, connID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byName := map[string]*TableConfig{}
	var tables []*TableConfig
	for rows.Next() {
		t := &TableConfig{Columns: map[string]string{}, UserIDs: []string{}}
		if err := rows.Scan(&t.Name, &t.Description); err != nil {
			return nil, nil, err
		}
		byName[t.Name] = t
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	colRows, err := s.db.Query(
		`SELECT table_name, column_name, col_type, description FROM connection_columns WHERE connection_id = ? ORDER BY rowid`, connID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer colRows.Close()
	for colRows.Next() {
		var tn, cn, ct, d string
		if err := colRows.Scan(&tn, &cn, &ct, &d); err != nil {
			return nil, nil, err
		}
		if t, ok := byName[tn]; ok {
			t.ColumnList = append(t.ColumnList, ColumnInfo{Name: cn, Type: ct, Description: d})
			if d != "" {
				t.Columns[cn] = d
			}
		}
	}

	accRows, err := s.db.Query(
		`SELECT table_name, user_id FROM connection_access WHERE connection_id = ?`, connID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer accRows.Close()
	for accRows.Next() {
		var tn, uid string
		if err := accRows.Scan(&tn, &uid); err != nil {
			return nil, nil, err
		}
		if t, ok := byName[tn]; ok {
			t.UserIDs = append(t.UserIDs, uid)
		}
	}

	linkRows, err := s.db.Query(
		`SELECT from_table, from_column, to_table, to_column FROM connection_links WHERE connection_id = ?`, connID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer linkRows.Close()
	var links []LinkConfig
	for linkRows.Next() {
		var l LinkConfig
		if err := linkRows.Scan(&l.FromTable, &l.FromColumn, &l.ToTable, &l.ToColumn); err != nil {
			return nil, nil, err
		}
		links = append(links, l)
	}

	out := make([]TableConfig, 0, len(tables))
	for _, t := range tables {
		out = append(out, *t)
	}
	return out, links, nil
}

// AccessibleTables returnerer tabellene en gitt bruker har tilgang til
// (tabeller uten access-rader er åpne for alle i tenanten).
func (s *Store) AccessibleTables(connID, userID string) ([]TableConfig, []LinkConfig, error) {
	tables, links, err := s.ConnectionConfig(connID)
	if err != nil {
		return nil, nil, err
	}
	var open []TableConfig
	allowed := map[string]bool{}
	for _, t := range tables {
		if len(t.UserIDs) == 0 {
			open = append(open, t)
			allowed[t.Name] = true
			continue
		}
		for _, uid := range t.UserIDs {
			if uid == userID {
				open = append(open, t)
				allowed[t.Name] = true
				break
			}
		}
	}
	var openLinks []LinkConfig
	for _, l := range links {
		if allowed[l.FromTable] && allowed[l.ToTable] {
			openLinks = append(openLinks, l)
		}
	}
	return open, openLinks, nil
}
