// Package store er datalaget: tenants (kunder), users (ansatte),
// engangskoder og sesjoner. SQLite uten CGO — all data blir i EU.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("ikke funnet")
	ErrInvalidCode = errors.New("ugyldig eller utløpt kode")
)

const (
	codeTTL    = 10 * time.Minute
	sessionTTL = 30 * 24 * time.Hour
)

type Store struct {
	db *DB
}

// IsPostgres: sant når datalaget kjører mot Postgres (pgvector-henting mm.).
func (s *Store) IsPostgres() bool { return s.db.pg }

type Tenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type User struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

// Open åpner datalaget: en postgres://-URL gir Postgres (pgvector, tsvector),
// alt annet tolkes som SQLite-filsti. Skjemaene holdes i synk hver for seg —
// pgschema.sql er sannheten for Postgres, migrate*-funksjonene for SQLite.
func Open(path string) (*Store, error) {
	if strings.HasPrefix(path, "postgres://") || strings.HasPrefix(path, "postgresql://") {
		return openPostgres(path)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: én skriver
	s := &Store{db: &DB{sql: db}}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.migrateUsage(); err != nil {
		return nil, err
	}
	if err := s.migrateChats(); err != nil {
		return nil, err
	}
	if err := s.migrateConnections(); err != nil {
		return nil, err
	}
	if err := s.migrateCorrections(); err != nil {
		return nil, err
	}
	if err := s.migrateAgents(); err != nil {
		return nil, err
	}
	if err := s.migrateBrain(); err != nil {
		return nil, err
	}
	if err := s.migrateDesignBoard(); err != nil {
		return nil, err
	}
	if err := s.migrateWidgets(); err != nil {
		return nil, err
	}
	if err := s.migrateKnowledge(); err != nil {
		return nil, err
	}
	if err := s.migrateDocuments(); err != nil {
		return nil, err
	}
	if err := s.migrateNotes(); err != nil {
		return nil, err
	}
	if err := s.migrateExportLinks(); err != nil {
		return nil, err
	}
	if err := s.migrateM365(); err != nil {
		return nil, err
	}
	if err := s.migrateOneDrive(); err != nil {
		return nil, err
	}
	if err := s.migrateEmployees(); err != nil {
		return nil, err
	}
	if err := s.migrateOrg(); err != nil {
		return nil, err
	}
	if err := s.migrateNoteScope(); err != nil {
		return nil, err
	}
	return s, s.migrateIntentDecisions()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS tenants (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS users (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL REFERENCES tenants(id),
			email      TEXT NOT NULL UNIQUE,
			role       TEXT NOT NULL DEFAULT 'member',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS login_codes (
			email      TEXT NOT NULL,
			code       TEXT NOT NULL,
			expires_at TIMESTAMP NOT NULL,
			used       INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES users(id),
			expires_at TIMESTAMP NOT NULL
		);
	`)
	return err
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) CreateTenant(name string) (Tenant, error) {
	id, err := newID()
	if err != nil {
		return Tenant{}, err
	}
	t := Tenant{ID: id, Name: name}
	_, err = s.db.Exec(`INSERT INTO tenants (id, name) VALUES (?, ?)`, t.ID, t.Name)
	return t, err
}

func (s *Store) CreateUser(tenantID, email, role string) (User, error) {
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	u := User{ID: id, TenantID: tenantID, Email: email, Role: role}
	_, err = s.db.Exec(
		`INSERT INTO users (id, tenant_id, email, role) VALUES (?, ?, ?, ?)`,
		u.ID, u.TenantID, u.Email, u.Role,
	)
	return u, err
}

func (s *Store) UserByEmail(email string) (User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT id, tenant_id, email, role FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.TenantID, &u.Email, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// UserByID henter en bruker på id (brukes av oppdrags-agenten for å finne
// eierens e-postkonto ved autonom sending).
func (s *Store) UserByID(id string) (User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT id, tenant_id, email, role FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.TenantID, &u.Email, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) TenantByID(id string) (Tenant, error) {
	var t Tenant
	err := s.db.QueryRow(`SELECT id, name FROM tenants WHERE id = ?`, id).
		Scan(&t.ID, &t.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// CreateLoginCode lager en 6-sifret engangskode for e-posten.
func (s *Store) CreateLoginCode(email string) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", (uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3]))%1000000)
	_, err := s.db.Exec(
		`INSERT INTO login_codes (email, code, expires_at) VALUES (?, ?, ?)`,
		email, code, time.Now().Add(codeTTL),
	)
	return code, err
}

// RedeemCode validerer koden og bytter den mot en sesjonstoken.
func (s *Store) RedeemCode(email, code string) (string, User, error) {
	// Atomisk innløsning: UPDATE-en både finner og bruker koden i ett steg —
	// ingen les-endre-skriv-luke, portabelt uten SQLite-rowid.
	res, err := s.db.Exec(
		`UPDATE login_codes SET used = TRUE
		 WHERE email = ? AND code = ? AND used = FALSE AND expires_at > ?`,
		email, code, time.Now(),
	)
	if err != nil {
		return "", User{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", User{}, ErrInvalidCode
	}

	user, err := s.UserByEmail(email)
	if err != nil {
		return "", User{}, err
	}

	t1, err := newID()
	if err != nil {
		return "", User{}, err
	}
	t2, err := newID()
	if err != nil {
		return "", User{}, err
	}
	token := t1 + t2
	if _, err := s.db.Exec(
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, user.ID, time.Now().Add(sessionTTL),
	); err != nil {
		return "", User{}, err
	}
	return token, user, nil
}

// UserBySession returnerer brukeren for en gyldig sesjonstoken.
func (s *Store) UserBySession(token string) (User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT u.id, u.tenant_id, u.email, u.role
		 FROM sessions se JOIN users u ON u.id = se.user_id
		 WHERE se.token = ? AND se.expires_at > ?`,
		token, time.Now(),
	).Scan(&u.ID, &u.TenantID, &u.Email, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ListUsers returnerer alle brukere i en tenant.
func (s *Store) ListUsers(tenantID string) ([]User, error) {
	rows, err := s.db.Query(
		`SELECT id, tenant_id, email, role FROM users WHERE tenant_id = ? ORDER BY created_at`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Email, &u.Role); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser fjerner en bruker og alle sesjonene deres.
func (s *Store) DeleteUser(tenantID, userID string) error {
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ? AND tenant_id = ?`, userID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
