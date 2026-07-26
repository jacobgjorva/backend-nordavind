package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// M365Account er en brukers Microsoft 365-kobling (Graph). Refresh-tokenet
// lagres kryptert; access-tokens hentes ferskt ved behov og lagres aldri.
type M365Account struct {
	UserID     string    `json:"-"`
	TenantID   string    `json:"-"`
	Email      string    `json:"email"`
	RefreshEnc []byte    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) migrateM365() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS m365_accounts (
			user_id     TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			email       TEXT NOT NULL DEFAULT '',
			refresh_enc BLOB NOT NULL,
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS m365_apps (
			tenant_id  TEXT PRIMARY KEY,
			client_id  TEXT NOT NULL,
			secret_enc BLOB NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS m365_states (
			state      TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			expires_at TIMESTAMP NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	// Bakoverkompatibelt: aad_tenant på eldre m365_apps.
	if _, err := s.db.Exec(
		`ALTER TABLE m365_apps ADD COLUMN aad_tenant TEXT NOT NULL DEFAULT 'common'`,
	); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
}

// SaveM365State lagrer OAuth-staten i DB så den overlever server-omstart —
// innlogginger skal aldri dø fordi prosessen restartet midt i flyten.
func (s *Store) SaveM365State(state, tenantID, userID string, expires time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO m365_states (state, tenant_id, user_id, expires_at) VALUES (?, ?, ?, ?)`,
		state, tenantID, userID, expires,
	)
	return err
}

// TakeM365State henter og sletter staten (engangsbruk). ErrNotFound ved ukjent/utløpt.
func (s *Store) TakeM365State(state string) (tenantID, userID string, err error) {
	var expires time.Time
	err = s.db.QueryRow(
		`SELECT tenant_id, user_id, expires_at FROM m365_states WHERE state = ?`, state,
	).Scan(&tenantID, &userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	s.db.Exec(`DELETE FROM m365_states WHERE state = ?`, state)
	s.db.Exec(`DELETE FROM m365_states WHERE expires_at < CURRENT_TIMESTAMP`)
	if time.Now().After(expires) {
		return "", "", ErrNotFound
	}
	return tenantID, userID, nil
}

// SetM365App lagrer tenantens Azure app-registrering (client-id, kryptert secret
// og Azure directory/tenant-id) — samlet inn av connector-agenten.
func (s *Store) SetM365App(tenantID, clientID, aadTenant string, secretEnc []byte) error {
	if aadTenant == "" {
		aadTenant = "common"
	}
	_, err := s.db.Exec(
		`INSERT INTO m365_apps (tenant_id, client_id, aad_tenant, secret_enc) VALUES (?, ?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET client_id=excluded.client_id,
		   aad_tenant=excluded.aad_tenant, secret_enc=excluded.secret_enc`,
		tenantID, clientID, aadTenant, secretEnc,
	)
	return err
}

// M365App henter tenantens app-registrering.
func (s *Store) M365App(tenantID string) (clientID, aadTenant string, secretEnc []byte, err error) {
	err = s.db.QueryRow(
		`SELECT client_id, aad_tenant, secret_enc FROM m365_apps WHERE tenant_id = ?`, tenantID,
	).Scan(&clientID, &aadTenant, &secretEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, ErrNotFound
	}
	if aadTenant == "" {
		aadTenant = "common"
	}
	return clientID, aadTenant, secretEnc, err
}

// SetM365Account lagrer/erstatter brukerens M365-kobling.
func (s *Store) SetM365Account(tenantID, userID, email string, refreshEnc []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO m365_accounts (user_id, tenant_id, email, refresh_enc)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET email=excluded.email, refresh_enc=excluded.refresh_enc`,
		userID, tenantID, email, refreshEnc,
	)
	return err
}

// M365Account henter brukerens M365-kobling.
func (s *Store) M365Account(userID string) (M365Account, error) {
	var a M365Account
	a.UserID = userID
	err := s.db.QueryRow(
		`SELECT tenant_id, email, refresh_enc, created_at FROM m365_accounts WHERE user_id = ?`, userID,
	).Scan(&a.TenantID, &a.Email, &a.RefreshEnc, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// DeleteM365Account kobler fra Microsoft 365 for brukeren.
func (s *Store) DeleteM365Account(userID string) error {
	_, err := s.db.Exec(`DELETE FROM m365_accounts WHERE user_id = ?`, userID)
	return err
}
