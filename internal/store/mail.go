package store

import "time"

// MailAccount er én e-postkonto (IMAP/SMTP). I dev leses den fra config;
// structen brukes som retur-/JSON-form av API-laget.
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
