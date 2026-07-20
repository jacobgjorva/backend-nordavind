// Package mail sender e-post via SMTP. Lesing fra innboksen (IMAP) er bevisst
// fjernet — kun send-evnen beholdes for framtidig bruk.
package mail

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"time"

	"github.com/emersion/go-message/mail"
)

// Account er tilkoblingsdetaljene (dekryptert av kalleren). IMAP-feltene
// beholdes i strukturen selv om lesing ikke lenger støttes.
type Account struct {
	Email    string
	IMAPHost string
	IMAPPort int
	SMTPHost string
	SMTPPort int
	Password string
}

// Person er en avsender/mottaker.
type Person struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// OutAttachment er et utgående vedlegg.
type OutAttachment struct {
	Filename string
	Type     string
	Data     []byte
}

// OutMessage er en e-post som skal sendes.
type OutMessage struct {
	To          []Person
	Cc          []Person
	Bcc         []Person
	Subject     string
	Body        string // ren tekst (signatur inkludert av kalleren)
	InReplyTo   string // Message-ID det svares på (valgfritt)
	References  string // References-kjede (valgfritt)
	Attachments []OutAttachment
}

func addrList(ps []Person) []*mail.Address {
	out := make([]*mail.Address, 0, len(ps))
	for _, p := range ps {
		out = append(out, &mail.Address{Name: p.Name, Address: p.Address})
	}
	return out
}

// build lager RFC-meldingen (MIME) og returnerer rå-bytes + alle mottakere.
func (a Account) build(m OutMessage) ([]byte, []string, error) {
	var buf bytes.Buffer
	var h mail.Header
	h.SetDate(time.Now())
	h.SetAddressList("From", []*mail.Address{{Address: a.Email}})
	if len(m.To) > 0 {
		h.SetAddressList("To", addrList(m.To))
	}
	if len(m.Cc) > 0 {
		h.SetAddressList("Cc", addrList(m.Cc))
	}
	h.SetSubject(m.Subject)
	if m.InReplyTo != "" {
		h.Set("In-Reply-To", m.InReplyTo)
	}
	if m.References != "" {
		h.Set("References", m.References)
	}

	mw, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return nil, nil, err
	}
	var th mail.InlineHeader
	th.Set("Content-Type", "text/plain; charset=utf-8")
	tw, err := mw.CreateSingleInline(th)
	if err != nil {
		return nil, nil, err
	}
	tw.Write([]byte(m.Body))
	tw.Close()

	for _, att := range m.Attachments {
		var ah mail.AttachmentHeader
		ah.Set("Content-Type", att.Type)
		ah.SetFilename(att.Filename)
		aw, err := mw.CreateAttachment(ah)
		if err != nil {
			return nil, nil, err
		}
		aw.Write(att.Data)
		aw.Close()
	}
	mw.Close()

	var rcpt []string
	for _, group := range [][]Person{m.To, m.Cc, m.Bcc} {
		for _, p := range group {
			if p.Address != "" {
				rcpt = append(rcpt, p.Address)
			}
		}
	}
	return buf.Bytes(), rcpt, nil
}

// Send sender meldingen via SMTP (STARTTLS på 587, implisitt TLS på 465).
func (a Account) Send(m OutMessage) error {
	raw, rcpt, err := a.build(m)
	if err != nil {
		return err
	}
	if len(rcpt) == 0 {
		return fmt.Errorf("ingen mottakere")
	}
	addr := fmt.Sprintf("%s:%d", a.SMTPHost, a.SMTPPort)
	auth := smtp.PlainAuth("", a.Email, a.Password, a.SMTPHost)

	if a.SMTPPort == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: a.SMTPHost})
		if err != nil {
			return fmt.Errorf("smtp-tilkobling feilet: %w", err)
		}
		c, err := smtp.NewClient(conn, a.SMTPHost)
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp-innlogging feilet: %w", err)
		}
		if err := c.Mail(a.Email); err != nil {
			return err
		}
		for _, r := range rcpt {
			if err := c.Rcpt(r); err != nil {
				return err
			}
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
		w.Close()
		return c.Quit()
	}
	// 587 / annet: SendMail håndterer STARTTLS.
	return smtp.SendMail(addr, auth, a.Email, rcpt, raw)
}
