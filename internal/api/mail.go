package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/mail"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// mailAccount bygger e-postkontoen. MIDLERTIDIG: én delt env-konto i dev.
// Gates hardt til cfg.Mail.Owner slik at kun den ene eier-brukeren når den —
// ingen andre tenanter/brukere skal kunne lese eller sende fra den.
// Flyttes til Connector-siden (per-bruker) på produksjonsnivå.
func (s *Server) mailAccount(u store.User) (mail.Account, store.MailAccount, error) {
	m := s.cfg.Mail
	if m.Email == "" || m.IMAPHost == "" || m.Password == "" {
		return mail.Account{}, store.MailAccount{}, store.ErrNotFound
	}
	if m.Owner == "" || !strings.EqualFold(u.Email, m.Owner) {
		return mail.Account{}, store.MailAccount{}, store.ErrNotFound
	}
	rec := store.MailAccount{
		Email:    m.Email,
		IMAPHost: m.IMAPHost, IMAPPort: m.IMAPPort,
		SMTPHost: m.SMTPHost, SMTPPort: m.SMTPPort,
		Signature: m.Signature,
	}
	return mail.Account{
		Email:    m.Email,
		IMAPHost: m.IMAPHost, IMAPPort: m.IMAPPort,
		SMTPHost: m.SMTPHost, SMTPPort: m.SMTPPort,
		Password: m.Password,
	}, rec, nil
}

// handleGetMailAccount returnerer konto-status (fra config).
func (s *Server) handleGetMailAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	_, rec, err := s.mailAccount(user)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ingen konto", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

// llmComplete kjører ett ikke-streamet LLM-kall og returnerer innholdet.
func (s *Server) llmComplete(ctx context.Context, system, userMsg string, maxTokens int) (string, error) {
	payload := map[string]any{
		"model": router.MidModel,
		"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": userMsg},
		},
		"stream":      false,
		"temperature": 0.3,
		"max_tokens":  maxTokens,
		"reasoning":   map[string]any{"enabled": false},
	}
	body, _ := json.Marshal(payload)
	req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("tomt svar")
	}
	return out.Choices[0].Message.Content, nil
}

// stripFences fjerner ```json ... ``` rundt et JSON-svar.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

// handleMailSend sender en e-post.
func (s *Server) handleMailSend(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	acc, rec, err := s.mailAccount(user)
	hasSMTP := err == nil
	var req struct {
		To          []mail.Person `json:"to"`
		Cc          []mail.Person `json:"cc"`
		Bcc         []mail.Person `json:"bcc"`
		Subject     string        `json:"subject"`
		Body        string        `json:"body"`
		BodyHTML    string        `json:"body_html"`
		Attachments []string      `json:"attachment_ids"`
		InReplyTo   string        `json:"in_reply_to"`
		References  string        `json:"references"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	if !hasSMTP {
		// Ingen SMTP-konto: send via Microsoft Graph når M365 er koblet —
		// samme kobling som filer/e-postlesing, null ekstra oppsett.
		if err := s.sendViaGraph(r.Context(), user.ID, req.To, req.Cc, req.Bcc, req.Subject, req.Body, req.BodyHTML, req.Attachments); err != nil {
			http.Error(w, "sending feilet: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body := req.Body
	if strings.TrimSpace(rec.Signature) != "" {
		body = body + "\n\n" + rec.Signature
	}
	err = acc.Send(mail.OutMessage{
		To: req.To, Cc: req.Cc, Bcc: req.Bcc,
		Subject: req.Subject, Body: body,
		InReplyTo: req.InReplyTo, References: req.References,
	})
	if err != nil {
		http.Error(w, "sending feilet: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mailBody velger HTML når composeren har formatert innhold, ellers ren tekst.
func mailBody(text, html string) map[string]any {
	if strings.TrimSpace(html) != "" {
		return map[string]any{"contentType": "HTML", "content": html}
	}
	return map[string]any{"contentType": "Text", "content": text}
}

// sendViaGraph sender e-posten med brukerens Microsoft 365-konto.
func (s *Server) sendViaGraph(ctx context.Context, userID string, to, cc, bcc []mail.Person, subject, body, bodyHTML string, attachmentIDs []string) error {
	token, err := s.msAccessToken(ctx, userID)
	if err != nil {
		return fmt.Errorf("Microsoft 365 er ikke koblet til")
	}
	rcpt := func(ps []mail.Person) []map[string]any {
		out := make([]map[string]any, 0, len(ps))
		for _, p := range ps {
			out = append(out, map[string]any{"emailAddress": map[string]any{"address": p.Address, "name": p.Name}})
		}
		return out
	}
	message := map[string]any{
		"subject":       subject,
		"body":          mailBody(body, bodyHTML),
		"toRecipients":  rcpt(to),
		"ccRecipients":  rcpt(cc),
		"bccRecipients": rcpt(bcc),
	}
	var atts []map[string]any
	for _, id := range attachmentIDs {
		if a, ok := s.mailAttach.get(userID, id); ok {
			atts = append(atts, map[string]any{
				"@odata.type":  "#microsoft.graph.fileAttachment",
				"name":         a.Name,
				"contentType":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
				"contentBytes": base64.StdEncoding.EncodeToString(a.Data),
			})
		}
	}
	if len(atts) > 0 {
		message["attachments"] = atts
	}
	payload := map[string]any{
		"message":         message,
		"saveToSentItems": true,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://graph.microsoft.com/v1.0/me/sendMail", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("e-posttilgang mangler — koble til Microsoft 365 på nytt")
	}
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Graph svarte %d", resp.StatusCode)
	}
	return nil
}
