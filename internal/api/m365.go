package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Microsoft 365-connector (Graph). Én kobling dekker OneDrive/SharePoint nå og
// kan utvides med mail/Teams senere (incremental consent). Refresh-tokenet
// lagres kryptert per bruker; access-tokens hentes ferskt og lagres aldri.

const msScopes = "offline_access User.Read Files.ReadWrite"

// msAuthBaseFor gir riktig innloggings-endepunkt: single-tenant Azure-apper
// krever sin egen directory-id (/common gir AADSTS50194).
func msAuthBaseFor(aadTenant string) string {
	if aadTenant == "" {
		aadTenant = "common"
	}
	return "https://login.microsoftonline.com/" + url.PathEscape(aadTenant) + "/oauth2/v2.0"
}

func (s *Server) msRedirectURL() string {
	return strings.TrimSuffix(s.cfg.PublicBaseURL, "/") + "/v1/m365/callback"
}

// msAppCreds henter tenantens Azure app-registrering: DB (samlet inn av
// connector-agenten) først, env-variabler som fallback.
func (s *Server) msAppCreds(tenantID string) (clientID, secret, aadTenant string, ok bool) {
	if id, aad, enc, err := s.store.M365App(tenantID); err == nil {
		if plain, err := connector.Decrypt(s.credsKey, enc); err == nil {
			return id, string(plain), aad, true
		}
	}
	if s.cfg.MSClientID != "" {
		return s.cfg.MSClientID, s.cfg.MSClientSecret, "common", true
	}
	return "", "", "", false
}

// handleM365Status forteller frontend om integrasjonen er konfigurert og om
// brukeren er koblet til.
func (s *Server) handleM365Status(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	_, _, _, hasApp := s.msAppCreds(user.TenantID)
	out := map[string]any{"configured": hasApp, "connected": false}
	if acc, err := s.store.M365Account(user.ID); err == nil {
		out["connected"] = true
		out["email"] = acc.Email
		out["created_at"] = acc.CreatedAt
	}
	writeJSON(w, out)
}

// msAuthURL lager en state-vernet autoriserings-URL for brukeren.
func (s *Server) msAuthURL(user store.User) (string, error) {
	clientID, _, aadTenant, ok := s.msAppCreds(user.TenantID)
	if !ok {
		return "", errors.New("app-registrering mangler")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	state := hex.EncodeToString(raw)
	// DB-lagret state: overlever server-omstart midt i innloggingen.
	if err := s.store.SaveM365State(state, user.TenantID, user.ID, time.Now().Add(10*time.Minute)); err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", s.msRedirectURL())
	q.Set("response_mode", "query")
	q.Set("scope", msScopes)
	q.Set("state", state)
	return msAuthBaseFor(aadTenant) + "/authorize?" + q.Encode(), nil
}

// handleM365Connect starter OAuth-flyten: returnerer autoriserings-URLen
// frontend åpner i nytt vindu.
func (s *Server) handleM365Connect(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	authURL, err := s.msAuthURL(user)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"url": authURL})
}

// msTokenResponse er feltene vi trenger fra token-endepunktet.
type msTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error_description"`
}

// msExchange bytter code/refresh_token mot tokens med tenantens app-creds.
func (s *Server) msExchange(ctx context.Context, tenantID string, form url.Values) (msTokenResponse, error) {
	clientID, secret, aadTenant, ok := s.msAppCreds(tenantID)
	if !ok {
		return msTokenResponse{}, errors.New("app-registrering mangler")
	}
	form.Set("client_id", clientID)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, msAuthBaseFor(aadTenant)+"/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return msTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return msTokenResponse{}, err
	}
	defer resp.Body.Close()
	var tr msTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return msTokenResponse{}, err
	}
	if tr.AccessToken == "" {
		return tr, errors.New(strings.TrimSpace(tr.Error))
	}
	return tr, nil
}

// msGraphMe henter brukerens e-post fra Graph (verifiserer samtidig tokenet).
func (s *Server) msGraphMe(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var me struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(body, &me) != nil {
		return ""
	}
	if me.Mail != "" {
		return me.Mail
	}
	return me.UserPrincipalName
}

// handleM365Callback fullfører OAuth: bytter koden, lagrer kryptert
// refresh-token og viser en liten «lukk fanen»-side.
func (s *Server) handleM365Callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	// Microsoft sender feilinfo i query ved avvist samtykke — logg alt synlig.
	if e := r.URL.Query().Get("error"); e != "" {
		s.log.Warn("m365 callback med feil fra Microsoft", "error", e,
			"desc", r.URL.Query().Get("error_description"))
	}
	tenantID, userID, err := s.store.TakeM365State(state)
	if err != nil || code == "" {
		s.log.Warn("m365 callback avvist", "kjent_state", err == nil, "har_code", code != "")
		http.Error(w, "ugyldig eller utløpt forespørsel — prøv å koble til på nytt", http.StatusBadRequest)
		return
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.msRedirectURL())
	form.Set("scope", msScopes)
	tr, err := s.msExchange(r.Context(), tenantID, form)
	if err != nil {
		s.log.Warn("m365 token-bytte feilet", "err", err)
		http.Error(w, "kunne ikke fullføre Microsoft-innloggingen", http.StatusBadGateway)
		return
	}

	email := s.msGraphMe(r.Context(), tr.AccessToken)
	enc, err := connector.Encrypt(s.credsKey, []byte(tr.RefreshToken))
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetM365Account(tenantID, userID, email, enc); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	s.log.Info("m365 koblet til", "user", userID, "email", email)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><body style="font-family:system-ui;background:#0e0e10;color:#eee;display:flex;align-items:center;justify-content:center;height:100vh"><div style="text-align:center"><h2>Microsoft 365 er koblet til ✓</h2><p>Du kan lukke denne fanen og gå tilbake til Nordavind.</p></div></body></html>`))
}

// msAccessToken henter et ferskt access-token for brukeren via refresh-tokenet.
// Roterer Microsoft refresh-tokenet, lagres det nye.
func (s *Server) msAccessToken(ctx context.Context, userID string) (string, error) {
	acc, err := s.store.M365Account(userID)
	if err != nil {
		return "", err
	}
	plain, err := connector.Decrypt(s.credsKey, acc.RefreshEnc)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", string(plain))
	form.Set("scope", msScopes)
	tr, err := s.msExchange(ctx, acc.TenantID, form)
	if err != nil {
		return "", err
	}
	if tr.RefreshToken != "" && tr.RefreshToken != string(plain) {
		if enc, err := connector.Encrypt(s.credsKey, []byte(tr.RefreshToken)); err == nil {
			s.store.SetM365Account("", userID, acc.Email, enc)
		}
	}
	return tr.AccessToken, nil
}

// handleM365Disconnect kobler brukeren fra Microsoft 365.
func (s *Server) handleM365Disconnect(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteM365Account(user.ID); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSaveM365App lagrer tenantens Azure app-registrering fra det sikre
// panelet i chatten — secret krypteres, og verdiene går aldri via meldinger.
func (s *Server) handleSaveM365App(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		DirectoryID  string `json:"directory_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.ClientSecret) == "" ||
		strings.TrimSpace(req.DirectoryID) == "" {
		http.Error(w, "mangler client_id, client_secret eller directory_id", http.StatusBadRequest)
		return
	}
	enc, err := connector.Encrypt(s.credsKey, []byte(strings.TrimSpace(req.ClientSecret)))
	if err != nil {
		http.Error(w, "kryptering feilet", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetM365App(user.TenantID, strings.TrimSpace(req.ClientID),
		strings.TrimSpace(req.DirectoryID), enc); err != nil {
		http.Error(w, "kunne ikke lagre", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
