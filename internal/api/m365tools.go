package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
	"github.com/xuri/excelize/v2"
)

// M365-verktøy for chatten: søk i og les brukerens OneDrive/SharePoint-filer
// via Graph. Legges kun til når brukeren faktisk har koblet Microsoft 365.

var m365SearchTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "m365_search",
		"description": "Søk i brukerens OneDrive/SharePoint-filer (Microsoft 365). Bruk når brukeren spør " +
			"om egne filer, dokumenter, regneark eller presentasjoner. Returnerer navn, id og sist endret. " +
			"Bruk konkrete søkeord; query='*' lister de nyeste filene.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "søkeord, f.eks. filnavn eller tema"},
			},
			"required": []string{"query"},
		},
	},
}

var m365ReadTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name": "m365_read",
		"description": "Les innholdet i én fil fra brukerens OneDrive/SharePoint (id fra m365_search). " +
			"Støtter tekst, PDF, Word og Excel.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_id": map[string]any{"type": "string", "description": "filens id fra m365_search"},
				"name":    map[string]any{"type": "string", "description": "filnavnet (for riktig tolkning)"},
			},
			"required": []string{"file_id"},
		},
	},
}

// m365Connected er sann når brukeren har en aktiv M365-kobling.
func (s *Server) m365Connected(ctx context.Context) (string, bool) {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "", false
	}
	if _, err := s.store.M365Account(user.ID); err != nil {
		return "", false
	}
	return user.ID, true
}

// runM365Search søker i brukerens drive via Graph.
func (s *Server) runM365Search(ctx context.Context, userID, query string) string {
	query = strings.TrimSpace(query)
	token, err := s.msAccessToken(ctx, userID)
	if err != nil {
		return "Microsoft 365 er ikke koblet til."
	}
	// Jokertegn/tomt: Graph-søk støtter ikke '*' — list nylige filer i stedet.
	u := "https://graph.microsoft.com/v1.0/me/drive/root/search(q='" +
		url.PathEscape(strings.ReplaceAll(query, "'", "''")) + "')?$top=10&$select=id,name,size,webUrl,lastModifiedDateTime,file,folder"
	if query == "*" || query == "" {
		u = "https://graph.microsoft.com/v1.0/me/drive/recent?$top=10"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "Intern feil."
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return "Kunne ikke nå Microsoft Graph."
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		s.log.Warn("m365-søk feilet", "status", resp.StatusCode)
		return "Søket i OneDrive feilet."
	}
	var out struct {
		Value []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			WebURL   string `json:"webUrl"`
			Modified string `json:"lastModifiedDateTime"`
			Folder   *struct{} `json:"folder"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "Uventet svar fra Graph."
	}
	if len(out.Value) == 0 && query != "*" && query != "" {
		// Graph-søkeindeksen henger etter for nye filer — sjekk nylige og
		// filtrer på navn før vi gir opp.
		if rec := s.m365Recent(ctx, token, query); rec != "" {
			return rec
		}
		return "Ingen treff i OneDrive/SharePoint på «" + query + "»."
	}
	if len(out.Value) == 0 {
		return "Ingen treff i OneDrive/SharePoint."
	}
	var b strings.Builder
	b.WriteString("Treff (m365_read med id leser innholdet; url er direkte-lenke brukeren kan åpne):\n")
	for _, v := range out.Value {
		kind := "fil"
		if v.Folder != nil {
			kind = "mappe"
		}
		fmt.Fprintf(&b, "- %s (%s, id=%s, endret %s, url=%s)\n", v.Name, kind, v.ID, v.Modified, v.WebURL)
	}
	b.WriteString("Vil brukeren åpne/ha fila: del url-en som en klikkbar markdown-lenke [filnavn](url).")
	return b.String()
}

// m365Recent henter nylige filer og filtrerer på navn (fallback når
// søkeindeksen ikke har fanget nye filer ennå). Tom streng = ingen treff.
func (s *Server) m365Recent(ctx context.Context, token, nameFilter string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://graph.microsoft.com/v1.0/me/drive/recent?$top=25", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Value []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			WebURL   string `json:"webUrl"`
			Modified string `json:"lastModifiedDateTime"`
		} `json:"value"`
	}
	if json.Unmarshal(body, &out) != nil {
		return ""
	}
	needle := strings.ToLower(strings.TrimSpace(nameFilter))
	var b strings.Builder
	n := 0
	for _, v := range out.Value {
		if !strings.Contains(strings.ToLower(v.Name), needle) {
			continue
		}
		if n == 0 {
			b.WriteString("Treff blant nylige filer (m365_read med id leser innholdet; url er direkte-lenke):\n")
		}
		fmt.Fprintf(&b, "- %s (id=%s, endret %s, url=%s)\n", v.Name, v.ID, v.Modified, v.WebURL)
		n++
	}
	if n > 0 {
		b.WriteString("Vil brukeren åpne/ha fila: del url-en som en klikkbar markdown-lenke [filnavn](url).")
	}
	return b.String()
}

// runM365Read henter og tekst-ekstraherer én fil.
func (s *Server) runM365Read(ctx context.Context, userID, fileID, name string) string {
	if strings.TrimSpace(fileID) == "" {
		return "Mangler file_id."
	}
	token, err := s.msAccessToken(ctx, userID)
	if err != nil {
		return "Microsoft 365 er ikke koblet til."
	}
	u := "https://graph.microsoft.com/v1.0/me/drive/items/" + url.PathEscape(fileID) + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "Intern feil."
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return "Kunne ikke nå Microsoft Graph."
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Kunne ikke hente filen (status %d).", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "Kunne ikke lese filen."
	}

	const maxChars = 6000
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		if t := search.ExtractPDF(raw, maxChars); t != "" {
			return t
		}
		return "Klarte ikke å hente tekst fra PDF-en."
	case strings.HasSuffix(lower, ".docx"):
		if t := search.ExtractDOCX(raw, maxChars); t != "" {
			return t
		}
		return "Klarte ikke å hente tekst fra dokumentet."
	case strings.HasSuffix(lower, ".xlsx"):
		return extractXLSX(raw, maxChars)
	default:
		// Antatt tekstlig (txt/csv/md/json). Binært innhold gir uleselig svar —
		// modellen ser det selv og kan si fra.
		t := strings.TrimSpace(string(raw))
		if r := []rune(t); len(r) > maxChars {
			t = string(r[:maxChars])
		}
		if t == "" {
			return "Filen er tom eller i et format jeg ikke kan lese."
		}
		return t
	}
}

// extractXLSX gir en kompakt tekstlig gjengivelse av første ark.
func extractXLSX(raw []byte, maxChars int) string {
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		return "Klarte ikke å åpne regnearket."
	}
	defer f.Close()
	sheet := f.GetSheetName(0)
	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		return "Regnearket er tomt."
	}
	var b strings.Builder
	for _, r := range rows {
		line := strings.Join(r, "\t")
		if b.Len()+len(line) > maxChars {
			b.WriteString("… (avkortet)")
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
