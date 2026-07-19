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
	conns  map[string]dbConn // connection-id -> tilkobling
	tool   map[string]any
	schema string // menneskelesbart skjema (til dashboard-SQL)
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
	return s.buildDBTool(user.TenantID, user.ID, "")
}

// buildDBTool bygger query_database-verktøyet for en tenant/bruker. Hvis
// onlyConnID er satt, begrenses verktøyet til den ene tilkoblingen (brukes
// av agent-scheduleren, som kjører uten en HTTP-sesjon).
func (s *Server) buildDBTool(tenantID, userID, onlyConnID string) *dbToolCtx {
	conns, err := s.store.ListConnections(tenantID)
	if err != nil || len(conns) == 0 {
		return nil
	}

	t := &dbToolCtx{conns: map[string]dbConn{}}
	var schema strings.Builder
	for _, c := range conns {
		if onlyConnID != "" && c.ID != onlyConnID {
			continue
		}
		tables, links, err := s.store.AccessibleTables(c.ID, userID)
		if err != nil {
			continue
		}
		views, _ := s.store.ConnectionViews(c.ID)
		if len(tables) == 0 && len(views) == 0 {
			continue
		}
		_, enc, err := s.store.ConnectionCreds(tenantID, c.ID)
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
			for _, col := range tb.ColumnList {
				fmt.Fprintf(&schema, "  - %s (%s)", col.Name, col.Type)
				if col.Description != "" {
					fmt.Fprintf(&schema, ": %s", col.Description)
				}
				schema.WriteString("\n")
			}
		}
		for _, l := range links {
			fmt.Fprintf(&schema, "JOIN: %s.%s = %s.%s\n", l.FromTable, l.FromColumn, l.ToTable, l.ToColumn)
		}
		for _, v := range views {
			fmt.Fprintf(&schema, "Ferdig spørring %q", v.Name)
			if v.Description != "" {
				fmt.Fprintf(&schema, " (%s)", v.Description)
			}
			fmt.Fprintf(&schema, ": %s\n", v.SQL)
			// Tabellene spørringen bruker må være kjørbare selv om de
			// ikke er valgt enkeltvis.
			allowed = append(allowed, connector.ReferencedTables(v.SQL)...)
		}
		t.conns[c.ID] = dbConn{conn: c, creds: creds, allowed: allowed}
	}
	if len(t.conns) == 0 {
		return nil
	}
	t.schema = schema.String()

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

	cols, rows, err := connector.SafeQuery(ctx, db, dc.creds.Driver, query, dc.allowed)
	if err != nil {
		return "Spørringen feilet: " + err.Error()
	}
	s.log.Info("db-spørring", "connection", dc.conn.Name, "rader", len(rows))

	out, _ := json.Marshal(map[string]any{"columns": cols, "rows": rows, "row_count": len(rows)})
	return string(out)
}
