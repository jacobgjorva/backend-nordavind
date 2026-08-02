package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// M365-dokumentsynk v1 (2026-08-02, design avtalt med Jacob): OneDrive →
// brukerens PRIVATE tre. Delegert token betyr at Microsoft selv håndhever
// hva som kan leses; alt som synkes får user:-scope, så ingenting deles
// videre uten brukerens/adminens bevisste valg. Delingslenker og
// enkeltfil-ACL tolkes ALDRI — SharePoint-sites med site→enhet-mapping er
// v2 når org-strukturen er moden.

const (
	syncMaxFiles = 40
	syncMaxBytes = 8 << 20
)

var syncExts = map[string]bool{
	".pdf": true, ".docx": true, ".txt": true, ".md": true, ".csv": true,
}

// handleM365SyncDocs starter synken i bakgrunnen og svarer med en gang.
func (s *Server) handleM365SyncDocs(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	// Uten v2-henting håndheves ikke scope — synk til privat tre ville
	// lekket via v1-veien. Hard vakt.
	if s.cfg.KnowledgeMode != "v2" {
		writeErr(w, http.StatusBadRequest, "krever kunnskapsgraf v2")
		return
	}
	if _, err := s.msAccessToken(r.Context(), user.ID); err != nil {
		writeErr(w, http.StatusBadRequest, "Microsoft 365 er ikke koblet til")
		return
	}
	go s.runM365DocSync(user)
	writeJSON(w, map[string]any{"started": true})
}

func (s *Server) runM365DocSync(user store.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	token, err := s.msAccessToken(ctx, user.ID)
	if err != nil {
		return
	}
	items, err := s.graphRecentFiles(ctx, token)
	if err != nil {
		s.log.Warn("m365-synk: kunne ikke liste filer", "err", err)
		return
	}
	synced, skipped := 0, 0
	for _, it := range items {
		if synced >= syncMaxFiles {
			break
		}
		ext := strings.ToLower(it.Name[strings.LastIndex(it.Name, ".")+1:])
		if !syncExts["."+ext] || it.Size > syncMaxBytes {
			continue
		}
		if s.store.HasSyncedDoc(user.TenantID, user.ID, it.ID, it.Modified) {
			skipped++
			continue
		}
		raw, err := s.graphDownload(ctx, token, it.ID)
		if err != nil {
			continue
		}
		text := extractText(it.Name, raw)
		if strings.TrimSpace(text) == "" {
			continue
		}
		_, _, err = s.ingestDocument(ctx, user, it.Name, text, store.UserScope(user.ID), store.DocumentInput{
			Owner: user.ID, OriginID: it.ID, OriginMod: it.Modified,
		})
		if err != nil {
			s.log.Warn("m365-synk: inngest feilet", "fil", it.Name, "err", err)
			continue
		}
		synced++
	}
	s.log.Info("m365-synk ferdig", "bruker", user.Email, "synket", synced, "uendret", skipped)
}

type driveItem struct {
	ID       string
	Name     string
	Size     int64
	Modified string
}

// graphRecentFiles: brukerens nylig brukte filer — v1-utvalget. Aktive
// dokumenter er de som betyr noe; full drive-vandring er v2.
func (s *Server) graphRecentFiles(ctx context.Context, token string) ([]driveItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://graph.microsoft.com/v1.0/me/drive/recent?$top=50", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Value []struct {
			ID                   string `json:"id"`
			Name                 string `json:"name"`
			Size                 int64  `json:"size"`
			LastModifiedDateTime string `json:"lastModifiedDateTime"`
			RemoteItem           *struct {
				ID string `json:"id"`
			} `json:"remoteItem"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	items := make([]driveItem, 0, len(out.Value))
	for _, v := range out.Value {
		id := v.ID
		if v.RemoteItem != nil && v.RemoteItem.ID != "" {
			// Delte elementer peker ut av brukerens egen drive — v1 synker
			// KUN eget innhold (privat tre); delte hopper vi over.
			continue
		}
		items = append(items, driveItem{ID: id, Name: v.Name, Size: v.Size, Modified: v.LastModifiedDateTime})
	}
	return items, nil
}

func (s *Server) graphDownload(ctx context.Context, token, fileID string) ([]byte, error) {
	u := "https://graph.microsoft.com/v1.0/me/drive/items/" + url.PathEscape(fileID) + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errHTTP(resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, syncMaxBytes))
}

func extractText(name string, raw []byte) string {
	const maxChars = 60000
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return search.ExtractPDF(raw, maxChars)
	case strings.HasSuffix(lower, ".docx"):
		return search.ExtractDOCX(raw, maxChars)
	default:
		t := string(raw)
		if len(t) > maxChars {
			t = t[:maxChars]
		}
		return t
	}
}

type httpErr int

func (e httpErr) Error() string { return "HTTP " + http.StatusText(int(e)) }
func errHTTP(code int) error    { return httpErr(code) }
