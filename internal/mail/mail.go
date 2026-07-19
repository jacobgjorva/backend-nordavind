// Package mail kobler mot en brukers e-postkonto via IMAP (lesing) og SMTP
// (sending). Trådene grupperes på normalisert emne.
package mail

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net/smtp"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
)

// Account er tilkoblingsdetaljene (dekryptert av kalleren).
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


// ThreadSummary er én tråd i inbox-listen.
type ThreadSummary struct {
	Key       string    `json:"key"` // normalisert emne
	Subject   string    `json:"subject"`
	From      Person    `json:"from"`    // siste avsender
	Snippet   string    `json:"snippet"` // kort utdrag
	Date      time.Time `json:"date"`
	Count     int       `json:"count"`
	Unread    int       `json:"unread"`
	Attach    bool      `json:"attach"`
}

// Attachment er metadata om ett vedlegg (innhold lastes ikke i lista).
type Attachment struct {
	Filename string `json:"filename"`
	Type     string `json:"type"`
	Size     int    `json:"size"`
}

// Message er én melding i en tråd, med brødtekst.
type Message struct {
	UID       uint32       `json:"uid"`
	MessageID string       `json:"message_id"`
	From      Person       `json:"from"`
	To        []Person     `json:"to"`
	Cc        []Person     `json:"cc"`
	Date      time.Time    `json:"date"`
	Subject   string       `json:"subject"`
	Body      string       `json:"body"`
	Attach    []Attachment `json:"attachments"`
	Unread    bool         `json:"unread"`
}

var reSubjPrefix = regexp.MustCompile(`(?i)^\s*((re|sv|svar|fwd|fw|vs)\s*:\s*)+`)

// normSubject fjerner Re:/Fwd:-prefikser så en tråd får én nøkkel.
func normSubject(s string) string {
	s = reSubjPrefix.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.ToLower(s))
}

func toPerson(a imap.Address) Person {
	name := a.Name
	if dec, err := new(mime.WordDecoder).DecodeHeader(name); err == nil {
		name = dec
	}
	return Person{Name: name, Address: a.Addr()}
}

func toPersons(as []imap.Address) []Person {
	out := make([]Person, 0, len(as))
	for _, a := range as {
		out = append(out, toPerson(a))
	}
	return out
}

// dialIMAP kobler til og logger inn.
func (a Account) dialIMAP() (*imapclient.Client, error) {
	addr := fmt.Sprintf("%s:%d", a.IMAPHost, a.IMAPPort)
	c, err := imapclient.DialTLS(addr, nil)
	if err != nil {
		return nil, fmt.Errorf("imap-tilkobling feilet: %w", err)
	}
	if err := c.Login(a.Email, a.Password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("innlogging feilet: %w", err)
	}
	return c, nil
}


type envInfo struct {
	uid     uint32
	subject string
	from    Person
	date    time.Time
	unread  bool
	attach  bool
}

// fetchEnvelopes henter de siste n meldingene i INBOX (uten brødtekst).
func (a Account) fetchEnvelopes(n int) ([]envInfo, error) {
	c, err := a.dialIMAP()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	defer func() { c.Logout().Wait() }()

	mbox, err := c.Select("INBOX", nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("kunne ikke åpne INBOX: %w", err)
	}
	if mbox.NumMessages == 0 {
		return nil, nil
	}
	from := uint32(1)
	if mbox.NumMessages > uint32(n) {
		from = mbox.NumMessages - uint32(n) + 1
	}
	var seq imap.SeqSet
	seq.AddRange(from, mbox.NumMessages)
	opts := &imap.FetchOptions{
		UID:           true,
		Envelope:      true,
		Flags:         true,
		BodyStructure: &imap.FetchItemBodyStructure{},
	}
	msgs, err := c.Fetch(seq, opts).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch feilet: %w", err)
	}
	out := make([]envInfo, 0, len(msgs))
	for _, m := range msgs {
		if m.Envelope == nil {
			continue
		}
		var frm Person
		if len(m.Envelope.From) > 0 {
			frm = toPerson(m.Envelope.From[0])
		}
		unread := true
		for _, f := range m.Flags {
			if f == imap.FlagSeen {
				unread = false
			}
		}
		attach := false
		if m.BodyStructure != nil {
			m.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
				if disp := part.Disposition(); disp != nil && strings.EqualFold(disp.Value, "attachment") {
					attach = true
				}
				return true
			})
		}
		out = append(out, envInfo{
			uid: uint32(m.UID), subject: m.Envelope.Subject, from: frm,
			date: m.Envelope.Date, unread: unread, attach: attach,
		})
	}
	return out, nil
}

// Inbox grupperer de siste meldingene i tråder, nyest først.
func (a Account) Inbox(scan int) ([]ThreadSummary, error) {
	envs, err := a.fetchEnvelopes(scan)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*ThreadSummary{}
	for _, e := range envs {
		key := normSubject(e.subject)
		t := byKey[key]
		if t == nil {
			t = &ThreadSummary{Key: key, Subject: e.subject}
			byKey[key] = t
		}
		t.Count++
		if e.unread {
			t.Unread++
		}
		if e.attach {
			t.Attach = true
		}
		// Siste melding styrer emne/avsender/dato.
		if e.date.After(t.Date) {
			t.Date = e.date
			t.From = e.from
			if strings.TrimSpace(e.subject) != "" {
				t.Subject = e.subject
			}
		}
	}
	out := make([]ThreadSummary, 0, len(byKey))
	for _, t := range byKey {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

// Thread henter alle meldinger (med brødtekst) i tråden med gitt nøkkel.
func (a Account) Thread(key string, scan int) ([]Message, error) {
	envs, err := a.fetchEnvelopes(scan)
	if err != nil {
		return nil, err
	}
	var uids []imap.UID
	for _, e := range envs {
		if normSubject(e.subject) == key {
			uids = append(uids, imap.UID(e.uid))
		}
	}
	if len(uids) == 0 {
		return nil, nil
	}

	c, err := a.dialIMAP()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	defer func() { c.Logout().Wait() }()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return nil, err
	}

	seq := imap.UIDSetNum(uids...)
	opts := &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		Flags:       true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}
	msgs, err := c.Fetch(seq, opts).Collect()
	if err != nil {
		return nil, fmt.Errorf("trådhenting feilet: %w", err)
	}

	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		msg := Message{UID: uint32(m.UID)}
		if m.Envelope != nil {
			msg.Subject = m.Envelope.Subject
			msg.Date = m.Envelope.Date
			msg.MessageID = m.Envelope.MessageID
			if len(m.Envelope.From) > 0 {
				msg.From = toPerson(m.Envelope.From[0])
			}
			msg.To = toPersons(m.Envelope.To)
			msg.Cc = toPersons(m.Envelope.Cc)
		}
		msg.Unread = true
		for _, f := range m.Flags {
			if f == imap.FlagSeen {
				msg.Unread = false
			}
		}
		for _, buf := range m.BodySection {
			body, atts := parseBody(buf.Bytes)
			msg.Body = body
			msg.Attach = atts
		}
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// parseBody trekker ut ren tekst (foretrekker text/plain) og vedleggs-metadata.
func parseBody(raw []byte) (string, []Attachment) {
	mr, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		// Ikke MIME — returner rått.
		return string(raw), nil
	}
	var plain, html string
	var atts []Attachment
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch h := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := h.ContentType()
			b, _ := io.ReadAll(p.Body)
			if strings.EqualFold(ct, "text/plain") {
				plain += string(b)
			} else if strings.EqualFold(ct, "text/html") {
				html += string(b)
			}
		case *mail.AttachmentHeader:
			fn, _ := h.Filename()
			ct, _, _ := h.ContentType()
			b, _ := io.ReadAll(p.Body)
			atts = append(atts, Attachment{Filename: fn, Type: ct, Size: len(b)})
		}
	}
	body := plain
	if strings.TrimSpace(body) == "" {
		body = stripHTML(html)
	}
	return strings.TrimSpace(body), atts
}

var reTag = regexp.MustCompile(`(?s)<[^>]*>`)
var reWS = regexp.MustCompile(`[ \t]*\n[ \t\n]*`)

func stripHTML(s string) string {
	s = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</\w+>`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n\n")
	s = reTag.ReplaceAllString(s, "")
	return strings.TrimSpace(reWS.ReplaceAllString(s, "\n"))
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
