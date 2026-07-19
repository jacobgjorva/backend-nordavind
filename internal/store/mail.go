package store

import (
	"database/sql"
	"errors"
	"time"
)

// MailAccount er én brukers e-postkonto (IMAP/SMTP). Passord lagres kryptert
// (byttes ut med credsEnc av API-laget før lagring/etter henting).
type MailAccount struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	IMAPHost  string    `json:"imap_host"`
	IMAPPort  int       `json:"imap_port"`
	SMTPHost  string    `json:"smtp_host"`
	SMTPPort  int       `json:"smtp_port"`
	Signature string    `json:"signature"`
	UpdatedAt time.Time `json:"updated_at"`

	// Kun internt — aldri i JSON til klient.
	PassEnc []byte `json:"-"`
}

func (s *Store) migrateMail() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS mail_accounts (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL UNIQUE,
			email      TEXT NOT NULL,
			imap_host  TEXT NOT NULL,
			imap_port  INTEGER NOT NULL DEFAULT 993,
			smtp_host  TEXT NOT NULL,
			smtp_port  INTEGER NOT NULL DEFAULT 587,
			pass_enc   BLOB NOT NULL,
			signature  TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}

// SaveMailAccount oppretter eller oppdaterer brukerens e-postkonto.
func (s *Store) SaveMailAccount(userID string, a MailAccount) error {
	_, err := s.db.Exec(
		`INSERT INTO mail_accounts
			(id, user_id, email, imap_host, imap_port, smtp_host, smtp_port, pass_enc, signature, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
			email=excluded.email, imap_host=excluded.imap_host, imap_port=excluded.imap_port,
			smtp_host=excluded.smtp_host, smtp_port=excluded.smtp_port, pass_enc=excluded.pass_enc,
			signature=excluded.signature, updated_at=excluded.updated_at`,
		newID(), userID, a.Email, a.IMAPHost, a.IMAPPort, a.SMTPHost, a.SMTPPort,
		a.PassEnc, a.Signature, time.Now(),
	)
	return err
}

// MailAccount henter brukerens e-postkonto (med kryptert passord).
func (s *Store) MailAccount(userID string) (MailAccount, error) {
	var a MailAccount
	err := s.db.QueryRow(
		`SELECT id, email, imap_host, imap_port, smtp_host, smtp_port, pass_enc, signature, updated_at
		 FROM mail_accounts WHERE user_id = ?`,
		userID,
	).Scan(&a.ID, &a.Email, &a.IMAPHost, &a.IMAPPort, &a.SMTPHost, &a.SMTPPort,
		&a.PassEnc, &a.Signature, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// SetMailSignature oppdaterer kun signaturen.
func (s *Store) SetMailSignature(userID, sig string) error {
	res, err := s.db.Exec(
		`UPDATE mail_accounts SET signature = ?, updated_at = ? WHERE user_id = ?`,
		sig, time.Now(), userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteMailAccount fjerner brukerens e-postkonto.
func (s *Store) DeleteMailAccount(userID string) error {
	_, err := s.db.Exec(`DELETE FROM mail_accounts WHERE user_id = ?`, userID)
	return err
}
