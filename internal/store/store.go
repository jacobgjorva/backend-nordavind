// Package store er datalaget: tenants (kunder), users (ansatte),
// engangskoder og sesjoner. SQLite uten CGO — all data blir i EU.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	db *sql.DB
}

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

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: én skriver
	s := &Store{db: db}
	return s, s.migrate()
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

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) CreateTenant(name string) (Tenant, error) {
	t := Tenant{ID: newID(), Name: name}
	_, err := s.db.Exec(`INSERT INTO tenants (id, name) VALUES (?, ?)`, t.ID, t.Name)
	return t, err
}

func (s *Store) CreateUser(tenantID, email, role string) (User, error) {
	u := User{ID: newID(), TenantID: tenantID, Email: email, Role: role}
	_, err := s.db.Exec(
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
	rand.Read(b)
	code := fmt.Sprintf("%06d", (uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3]))%1000000)
	_, err := s.db.Exec(
		`INSERT INTO login_codes (email, code, expires_at) VALUES (?, ?, ?)`,
		email, code, time.Now().Add(codeTTL),
	)
	return code, err
}

// RedeemCode validerer koden og bytter den mot en sesjonstoken.
func (s *Store) RedeemCode(email, code string) (string, User, error) {
	var rowid int64
	err := s.db.QueryRow(
		`SELECT rowid FROM login_codes
		 WHERE email = ? AND code = ? AND used = 0 AND expires_at > ?
		 ORDER BY rowid DESC LIMIT 1`,
		email, code, time.Now(),
	).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", User{}, ErrInvalidCode
	}
	if err != nil {
		return "", User{}, err
	}

	user, err := s.UserByEmail(email)
	if err != nil {
		return "", User{}, err
	}

	if _, err := s.db.Exec(`UPDATE login_codes SET used = 1 WHERE rowid = ?`, rowid); err != nil {
		return "", User{}, err
	}

	token := newID() + newID()
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
