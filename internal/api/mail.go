package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/mail"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

const inboxScan = 80 // hvor mange meldinger som skannes for tråd-gruppering

// mailAccount bygger et dekryptert mail.Account for brukeren.
func (s *Server) mailAccount(userID string) (mail.Account, store.MailAccount, error) {
	rec, err := s.store.MailAccount(userID)
	if err != nil {
		return mail.Account{}, rec, err
	}
	pass, err := connector.Decrypt(s.credsKey, rec.PassEnc)
	if err != nil {
		return mail.Account{}, rec, err
	}
	return mail.Account{
		Email:    rec.Email,
		IMAPHost: rec.IMAPHost, IMAPPort: rec.IMAPPort,
		SMTPHost: rec.SMTPHost, SMTPPort: rec.SMTPPort,
		Password: string(pass),
	}, rec, nil
}

// handleSaveMailAccount lagrer/oppdaterer kontoen etter å ha verifisert IMAP.
func (s *Server) handleSaveMailAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	var req struct {
		Email     string `json:"email"`
		IMAPHost  string `json:"imap_host"`
		IMAPPort  int    `json:"imap_port"`
		SMTPHost  string `json:"smtp_host"`
		SMTPPort  int    `json:"smtp_port"`
		Password  string `json:"password"`
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	if req.IMAPPort == 0 {
		req.IMAPPort = 993
	}
	if req.SMTPPort == 0 {
		req.SMTPPort = 587
	}
	acc := mail.Account{
		Email:    req.Email,
		IMAPHost: req.IMAPHost, IMAPPort: req.IMAPPort,
		SMTPHost: req.SMTPHost, SMTPPort: req.SMTPPort,
		Password: req.Password,
	}
	if err := acc.Verify(); err != nil {
		http.Error(w, "kunne ikke koble til: "+err.Error(), http.StatusBadRequest)
		return
	}
	enc, err := connector.Encrypt(s.credsKey, []byte(req.Password))
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if err := s.store.SaveMailAccount(user.ID, store.MailAccount{
		Email:    req.Email,
		IMAPHost: req.IMAPHost, IMAPPort: req.IMAPPort,
		SMTPHost: req.SMTPHost, SMTPPort: req.SMTPPort,
		PassEnc: enc, Signature: req.Signature,
	}); err != nil {
		http.Error(w, "kunne ikke lagre", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetMailAccount returnerer konto-status (uten passord).
func (s *Server) handleGetMailAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	rec, err := s.store.MailAccount(user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ingen konto", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	rec.PassEnc = nil
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

func (s *Server) handleDeleteMailAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	s.store.DeleteMailAccount(user.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleMailInbox lister tråder nyest først.
func (s *Server) handleMailInbox(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	acc, _, err := s.mailAccount(user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ingen konto", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	threads, err := acc.Inbox(inboxScan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if threads == nil {
		threads = []mail.ThreadSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"threads": threads})
}

// handleMailThread returnerer meldingene i tråden + signatur.
func (s *Server) handleMailThread(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	acc, rec, err := s.mailAccount(user.ID)
	if err != nil {
		http.Error(w, "ingen konto", http.StatusNotFound)
		return
	}
	key := r.URL.Query().Get("key")
	msgs, err := acc.Thread(key, inboxScan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if msgs == nil {
		msgs = []mail.Message{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"messages":  msgs,
		"signature": rec.Signature,
		"me":        rec.Email,
	})
}

// llmComplete kjører ett ikke-streamet LLM-kall og returnerer innholdet.
func (s *Server) llmComplete(ctx context.Context, system, userMsg string) (string, error) {
	payload := map[string]any{
		"model": router.MidModel,
		"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": userMsg},
		},
		"stream":      false,
		"temperature": 0.3,
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

// threadText bygger en lesbar representasjon av tråden til LLM-en.
func threadText(msgs []mail.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&b, "--- Melding %d ---\n", i+1)
		fmt.Fprintf(&b, "Fra: %s <%s>\nDato: %s\n", m.From.Name, m.From.Address, m.Date.Format("2006-01-02 15:04"))
		if len(m.Attach) > 0 {
			names := make([]string, len(m.Attach))
			for j, a := range m.Attach {
				names[j] = a.Filename
			}
			fmt.Fprintf(&b, "Vedlegg: %s\n", strings.Join(names, ", "))
		}
		body := m.Body
		if len(body) > 4000 {
			body = body[:4000]
		}
		fmt.Fprintf(&b, "%s\n\n", body)
	}
	return b.String()
}

const mailAnalyzeSystem = "Du er en norsk e-postassistent. Du får en e-posttråd. Svar KUN med JSON " +
	"(ingen prat, ingen ```): {\"summary\": \"<én til to setninger om hva tråden handler om og hva som " +
	"kreves>\", \"essences\": [\"<essensen i melding 1, kun det man trenger for å svare>\", ...], " +
	"\"draft\": \"<et vennlig, profesjonelt svarutkast på norsk, uten signatur>\"}. " +
	"essences skal ha ett element per melding, i samme rekkefølge. Hold alt kort og konkret."

// handleMailAnalyze oppsummerer tråden og lager et svarutkast.
func (s *Server) handleMailAnalyze(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	acc, _, err := s.mailAccount(user.ID)
	if err != nil {
		http.Error(w, "ingen konto", http.StatusNotFound)
		return
	}
	key := r.URL.Query().Get("key")
	msgs, err := acc.Thread(key, inboxScan)
	if err != nil || len(msgs) == 0 {
		http.Error(w, "fant ikke tråden", http.StatusNotFound)
		return
	}
	raw, err := s.llmComplete(r.Context(), mailAnalyzeSystem, threadText(msgs))
	if err != nil {
		http.Error(w, "AI-analyse feilet", http.StatusBadGateway)
		return
	}
	var parsed struct {
		Summary  string   `json:"summary"`
		Essences []string `json:"essences"`
		Draft    string   `json:"draft"`
	}
	if err := json.Unmarshal([]byte(stripFences(raw)), &parsed); err != nil {
		// Fallback: bruk rå-teksten som sammendrag.
		parsed.Summary = strings.TrimSpace(raw)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(parsed)
}

// handleMailDraft forbedrer et svarutkast ut fra brukerens tilbakemelding.
func (s *Server) handleMailDraft(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	acc, _, err := s.mailAccount(user.ID)
	if err != nil {
		http.Error(w, "ingen konto", http.StatusNotFound)
		return
	}
	var req struct {
		Key      string `json:"key"`
		Current  string `json:"current"`
		Feedback string `json:"feedback"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	msgs, _ := acc.Thread(req.Key, inboxScan)
	sys := "Du er en norsk e-postassistent. Forbedre svarutkastet ut fra tilbakemeldingen. " +
		"Svar KUN med den nye e-postteksten (norsk, uten signatur), ingen forklaring."
	userMsg := fmt.Sprintf("Tråd:\n%s\n\nNåværende utkast:\n%s\n\nTilbakemelding: %s",
		threadText(msgs), req.Current, req.Feedback)
	draft, err := s.llmComplete(r.Context(), sys, userMsg)
	if err != nil {
		http.Error(w, "AI feilet", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"draft": strings.TrimSpace(draft)})
}

// handleMailSend sender en e-post.
func (s *Server) handleMailSend(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
		return
	}
	acc, rec, err := s.mailAccount(user.ID)
	if err != nil {
		http.Error(w, "ingen konto", http.StatusNotFound)
		return
	}
	var req struct {
		To         []mail.Person `json:"to"`
		Cc         []mail.Person `json:"cc"`
		Bcc        []mail.Person `json:"bcc"`
		Subject    string        `json:"subject"`
		Body       string        `json:"body"`
		InReplyTo  string        `json:"in_reply_to"`
		References string        `json:"references"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
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
