package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
)

// Vedlegg til utgående e-post: koden bygger Excel-fila fra en spørring når
// modellen setter opp send-kortet, og fila festes ved SENDING — som fortsatt
// kun skjer ved brukerens klikk. Filene ligger kort i minnet (TTL) per bruker.

type mailAttachment struct {
	UserID  string
	Name    string
	Data    []byte
	Created time.Time
}

type attachmentStore struct {
	mu    sync.Mutex
	items map[string]mailAttachment
}

const attachmentTTL = 30 * time.Minute

func (st *attachmentStore) put(userID, name string, data []byte) string {
	b := make([]byte, 8)
	rand.Read(b)
	id := hex.EncodeToString(b)
	st.mu.Lock()
	if st.items == nil {
		st.items = map[string]mailAttachment{}
	}
	// Rydd utløpte samtidig.
	for k, v := range st.items {
		if time.Since(v.Created) > attachmentTTL {
			delete(st.items, k)
		}
	}
	st.items[id] = mailAttachment{UserID: userID, Name: name, Data: data, Created: time.Now()}
	st.mu.Unlock()
	return id
}

func (st *attachmentStore) get(userID, id string) (mailAttachment, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	a, ok := st.items[id]
	if !ok || a.UserID != userID || time.Since(a.Created) > attachmentTTL {
		return mailAttachment{}, false
	}
	return a, true
}

// buildQueryAttachment kjører spørringen og bygger en Excel-fil i minnet.
// Returnerer vedleggs-id, radantall og evt. feilmelding til modellen.
func (s *Server) buildQueryAttachment(ctx context.Context, userID string, dbCtx *dbToolCtx, connID, sqlText, filename string) (string, string, int, string) {
	if dbCtx == nil {
		return "", "", 0, "Ingen database er tilgjengelig."
	}
	dc, _, ok, msg := dbCtx.resolveConn(connID, sqlText)
	if !ok {
		return "", "", 0, msg
	}
	db, err := connector.Open(ctx, dc.creds)
	if err != nil {
		return "", "", 0, "Kunne ikke koble til databasen: " + err.Error()
	}
	defer db.Close()
	cols, rows, err := connector.SafeQueryViewsN(ctx, db, dc.creds.Driver, sqlText, dc.allowed, dc.views, 5000)
	if err != nil {
		return "", "", 0, "Spørringen feilet: " + err.Error()
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "data"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		name += ".xlsx"
	}
	f, err := buildXLSX(strings.TrimSuffix(name, ".xlsx"), cols, rows)
	if err != nil {
		return "", "", 0, "Kunne ikke bygge Excel-fila."
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return "", "", 0, "Kunne ikke bygge Excel-fila."
	}
	id := s.mailAttach.put(userID, name, buf.Bytes())
	return id, name, len(rows), ""
}
