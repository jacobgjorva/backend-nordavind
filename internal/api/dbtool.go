package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// dbToolCtx er databasene brukeren har tilgang til i denne forespørselen.
type dbToolCtx struct {
	conns map[string]dbConn // connection-id -> tilkobling
	tool  map[string]any
}

type dbConn struct {
	conn    store.Connection
	creds   connector.Creds
	allowed []string
}

// dbToolContext bygger query_database-verktøyet for innlogget bruker.
// Returnerer nil hvis tenanten ikke har tilkoblinger brukeren kan bruke.
func (s *Server) dbToolContext(ctx context.Context) *dbToolCtx {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return nil
	}
	conns, err := s.store.ListConnections(user.TenantID)
	if err != nil || len(conns) == 0 {
		return nil
	}

	t := &dbToolCtx{conns: map[string]dbConn{}}
	var schema strings.Builder
	for _, c := range conns {
		tables, links, err := s.store.AccessibleTables(c.ID, user.ID)
		if err != nil || len(tables) == 0 {
			continue
		}
		_, enc, err := s.store.ConnectionCreds(user.TenantID, c.ID)
		if err != nil {
			continue
		}
		plain, err := connector.Decrypt(s.credsKey, enc)
		if err != nil {
			continue
		}
		var creds connector.Creds
		if err := json.Unmarshal(plain, &creds); err != nil {
			continue
		}

		var allowed []string
		fmt.Fprintf(&schema, "Database %q (id=%s, %s):\n", c.Name, c.ID, c.Driver)
		for _, tb := range tables {
			allowed = append(allowed, tb.Name)
			fmt.Fprintf(&schema, "- %s", tb.Name)
			if tb.Description != "" {
				fmt.Fprintf(&schema, ": %s", tb.Description)
			}
			schema.WriteString("\n")
			for col, desc := range tb.Columns {
				fmt.Fprintf(&schema, "  - %s: %s\n", col, desc)
			}
		}
		for _, l := range links {
			fmt.Fprintf(&schema, "JOIN: %s.%s = %s.%s\n", l.FromTable, l.FromColumn, l.ToTable, l.ToColumn)
		}
		t.conns[c.ID] = dbConn{conn: c, creds: creds, allowed: allowed}
	}
	if len(t.conns) == 0 {
		return nil
	}

	t.tool = map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "query_database",
			"description": "Kjør en SQL SELECT-spørring mot bedriftens interne database. Bruk denne " +
				"for alle spørsmål om bedriftens egne data. Kun SELECT; maks 100 rader returneres. " +
				"Bruk JOIN-nøklene under ved behov.\n\n" + schema.String(),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"connection_id": map[string]any{"type": "string", "description": "id for databasen"},
					"sql":           map[string]any{"type": "string", "description": "SELECT-spørringen"},
				},
				"required": []string{"connection_id", "sql"},
			},
		},
	}
	return t
}

// runDBQuery utfører et query_database-kall fra modellen.
func (s *Server) runDBQuery(ctx context.Context, t *dbToolCtx, connID, query string) string {
	if t == nil {
		return "Ingen database er tilgjengelig."
	}
	dc, ok := t.conns[connID]
	if !ok {
		// Én tilkobling: godta manglende/feil id.
		if len(t.conns) == 1 {
			for _, v := range t.conns {
				dc = v
			}
		} else {
			return "Ukjent connection_id."
		}
	}

	db, err := connector.Open(ctx, dc.creds)
	if err != nil {
		s.log.Warn("db-tilkobling feilet", "err", err)
		return "Kunne ikke koble til databasen: " + err.Error()
	}
	defer db.Close()

	cols, rows, err := connector.SafeQuery(ctx, db, query, dc.allowed)
	if err != nil {
		return "Spørringen feilet: " + err.Error()
	}
	s.log.Info("db-spørring", "connection", dc.conn.Name, "rader", len(rows))

	out, _ := json.Marshal(map[string]any{"columns": cols, "rows": rows, "row_count": len(rows)})
	return string(out)
}
