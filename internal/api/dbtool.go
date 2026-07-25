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
	// views: kuraterte utsnitt (navn → SQL). Kjøres ved server-side
	// ekspansjon — råtabellene bak er IKKE fritt spørrbare.
	views map[string]string
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
			s.log.Warn("db-verktøy: dropper kobling, tabelloppslag feilet", "connection", c.Name, "id", c.ID, "err", err)
			continue
		}
		views, _ := s.store.ConnectionViews(c.ID)
		// SIKKERHET: et utsnitt arver tilgangen til tabellen det er laget for
		// («<tabell>_query») — uten dette så brukere UTEN tabelltilgang likevel
		// alle utsnittene på koblingen.
		accessible := map[string]bool{}
		for _, tb := range tables {
			accessible[strings.ToLower(tb.Name)] = true
		}
		filtered := views[:0]
		for _, v := range views {
			base := strings.ToLower(strings.TrimSuffix(v.Name, "_query"))
			if accessible[base] {
				filtered = append(filtered, v)
			}
		}
		views = filtered
		if len(tables) == 0 && len(views) == 0 {
			s.log.Warn("db-verktøy: dropper kobling, ingen tilgjengelige tabeller/views", "connection", c.Name, "id", c.ID, "user", userID)
			continue
		}
		_, enc, err := s.store.ConnectionCreds(tenantID, c.ID)
		if err != nil {
			s.log.Warn("db-verktøy: dropper kobling, creds-oppslag feilet", "connection", c.Name, "id", c.ID, "err", err)
			continue
		}
		plain, err := connector.Decrypt(s.credsKey, enc)
		if err != nil {
			s.log.Warn("db-verktøy: dropper kobling, dekryptering feilet (feil SECRET_KEY?)", "connection", c.Name, "id", c.ID, "err", err)
			continue
		}
		var creds connector.Creds
		if err := json.Unmarshal(plain, &creds); err != nil {
			s.log.Warn("db-verktøy: dropper kobling, ugyldig creds-json", "connection", c.Name, "id", c.ID, "err", err)
			continue
		}

		var allowed []string
		// Tett format: én linje per tabell «navn(kol type, …)», beholder all
		// info men kutter punktlister/innrykk/linjeskift = færre tokens.
		fmt.Fprintf(&schema, "Database %q (id=%s, %s):\n", c.Name, c.ID, c.Driver)
		for _, tb := range tables {
			allowed = append(allowed, tb.Name)
			cols := make([]string, 0, len(tb.ColumnList))
			for _, col := range tb.ColumnList {
				s := col.Name + " " + col.Type
				if col.Description != "" {
					s += " (" + col.Description + ")"
				}
				cols = append(cols, s)
			}
			fmt.Fprintf(&schema, "%s(%s)", tb.Name, strings.Join(cols, ", "))
			if tb.Description != "" {
				fmt.Fprintf(&schema, " — %s", tb.Description)
			}
			schema.WriteString("\n")
		}
		for _, l := range links {
			fmt.Fprintf(&schema, "JOIN: %s.%s = %s.%s\n", l.FromTable, l.FromColumn, l.ToTable, l.ToColumn)
		}
		viewMap := map[string]string{}
		for _, v := range views {
			fmt.Fprintf(&schema, "Ferdig spørring %q", v.Name)
			if v.Description != "" {
				fmt.Fprintf(&schema, " (%s)", v.Description)
			}
			fmt.Fprintf(&schema, ": %s — spørres som FROM %s\n", v.SQL, v.Name)
			// SIKKERHET: råtabellene bak utsnittet legges ALDRI i allowed —
			// utsnittet kjøres ved server-side ekspansjon (SafeQueryViewsN),
			// ellers kunne modellen spørre forbi admin-tilgangen.
			viewMap[strings.ToLower(v.Name)] = strings.TrimSuffix(strings.TrimSpace(v.SQL), ";")
			allowed = append(allowed, v.Name)
		}
		t.conns[c.ID] = dbConn{conn: c, creds: creds, allowed: allowed, views: viewMap}
	}
	if len(t.conns) == 0 {
		s.log.Warn("db-verktøy: ingen brukbare koblinger, modellen får ingen database", "tenant", tenantID, "user", userID, "onlyConnID", onlyConnID)
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

// resolveConn velger tilkoblingen for en spørring deterministisk, uavhengig
// av om modellen gjettet riktig connection_id. Rekkefølge:
//  1. oppgitt id som faktisk har alle refererte tabeller
//  2. den ENE tilkoblingen som har alle refererte tabeller (auto-ruting)
//  3. oppgitt id (selv om tabeller mangler — la SafeQuery gi presis feil)
//  4. eneste tilkobling hvis det bare finnes én
//
// Returnerer (kobling, id, ok, feilmelding-til-modellen).
func (t *dbToolCtx) resolveConn(connID, query string) (dbConn, string, bool, string) {
	refs := connector.ReferencedTables(query)

	has := func(dc dbConn) bool {
		allowed := map[string]bool{}
		for _, a := range dc.allowed {
			allowed[strings.ToLower(a)] = true
		}
		for _, r := range refs {
			if !allowed[strings.ToLower(r)] {
				return false
			}
		}
		return true
	}

	// 1) Oppgitt id som dekker tabellene.
	if dc, ok := t.conns[connID]; ok && (len(refs) == 0 || has(dc)) {
		return dc, connID, true, ""
	}

	// 2) Auto-ruting: nøyaktig én kobling som har alle tabellene.
	if len(refs) > 0 {
		var match []string
		for id, dc := range t.conns {
			if has(dc) {
				match = append(match, id)
			}
		}
		if len(match) == 1 {
			return t.conns[match[0]], match[0], true, ""
		}
		if len(match) > 1 {
			return dbConn{}, "", false, fmt.Sprintf(
				"Tabellene finnes i flere databaser (%s). Oppgi riktig connection_id.\n%s",
				strings.Join(match, ", "), t.connectionList())
		}
	}

	// 3) Oppgitt id finnes, men mangler tabeller — la SafeQuery gi presis feil.
	if dc, ok := t.conns[connID]; ok {
		return dc, connID, true, ""
	}

	// 4) Bare én kobling totalt: bruk den uansett.
	if len(t.conns) == 1 {
		for id, dc := range t.conns {
			return dc, id, true, ""
		}
	}

	return dbConn{}, "", false, "Ukjent connection_id.\n" + t.connectionList()
}

// connectionList beskriver tilgjengelige databaser og tabellene deres, til
// bruk i feilmeldinger så modellen kan korrigere seg selv.
func (t *dbToolCtx) connectionList() string {
	var b strings.Builder
	b.WriteString("Tilgjengelige databaser:\n")
	for id, dc := range t.conns {
		fmt.Fprintf(&b, "- %s (id=%s): %s\n", dc.conn.Name, id, strings.Join(dc.allowed, ", "))
	}
	return b.String()
}

// runDBQuery utfører et query_database-kall fra modellen.
func (s *Server) runDBQuery(ctx context.Context, t *dbToolCtx, connID, query string) string {
	if t == nil {
		return "Ingen database er tilgjengelig."
	}
	dc, resolvedID, ok, msg := t.resolveConn(connID, query)
	if !ok {
		return msg
	}
	if resolvedID != connID {
		s.log.Info("db-spørring rutet om", "fra", connID, "til", resolvedID)
	}

	db, err := connector.Open(ctx, dc.creds)
	if err != nil {
		s.log.Warn("db-tilkobling feilet", "err", err)
		return "Kunne ikke koble til databasen: " + err.Error()
	}
	defer db.Close()

	cols, rows, err := connector.SafeQueryViewsN(ctx, db, dc.creds.Driver, query, dc.allowed, dc.views, 0)
	if err != nil {
		// Gi modellen nok kontekst til å rette seg selv i neste runde.
		msg := "Spørringen feilet: " + err.Error()
		if strings.Contains(err.Error(), "ikke tilgjengelig") {
			msg += "\nDenne databasen (" + dc.conn.Name + ") har: " + strings.Join(dc.allowed, ", ")
		}
		return msg
	}
	s.log.Info("db-spørring", "connection", dc.conn.Name, "rader", len(rows))

	out, _ := json.Marshal(map[string]any{"columns": cols, "rows": rows, "row_count": len(rows)})
	return string(out)
}
