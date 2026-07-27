package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	// columns: tabellnavn → kolonnenavn. Brukes til å gi modellen de FAKTISKE
	// kolonnene når en spørring bommer, i stedet for bare «does not exist».
	// Uten dette gjettet den samme feil om igjen (målt tre ganger på rad).
	columns map[string][]string
	// textColumns: tabellnavn → kolonner det gir mening å lete etter navn i.
	// Grunnlaget for å sjekke «finnes ikke»-svar før de slippes ut.
	textColumns map[string][]string
}

// dbToolContext bygger query_database-verktøyet for innlogget bruker.
// Returnerer nil hvis tenanten ikke har tilkoblinger brukeren kan bruke.
func (s *Server) dbToolContext(ctx context.Context) *dbToolCtx {
	return s.dbToolContextFor(ctx, "")
}

// dbToolContextFor tar med brukerens melding som fokus for tabellutvalget.
func (s *Server) dbToolContextFor(ctx context.Context, focus string) *dbToolCtx {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return nil
	}
	return s.buildDBToolFocused(user.TenantID, user.ID, "", focus)
}

// tableDetailLimit: over dette antallet tabeller sendes bare de mest
// relevante med kolonner. Under: alt, som før — de fleste baser er små, og
// da er full oversikt både billig og best.
const tableDetailLimit = 8

// relevantTables velger hvilke tabeller som skal vises med kolonner, ut fra
// ordene i brukerens melding. nil = alle (liten base).
func relevantTables(tables []store.TableConfig, focus string) map[string]bool {
	if len(tables) <= tableDetailLimit || strings.TrimSpace(focus) == "" {
		return nil
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(focus), func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9' ||
			r == 'æ' || r == 'ø' || r == 'å')
	}) {
		if len([]rune(w)) >= 3 {
			words[w] = true
		}
	}
	type scored struct {
		name  string
		score int
	}
	var list []scored
	for _, tb := range tables {
		sc := 0
		hay := strings.ToLower(tb.Name + " " + tb.Description)
		for w := range words {
			if strings.Contains(hay, w) {
				sc += 3
			}
		}
		for _, col := range tb.ColumnList {
			cl := strings.ToLower(col.Name)
			for w := range words {
				if strings.Contains(cl, w) {
					sc++
				}
			}
		}
		list = append(list, scored{tb.Name, sc})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	out := map[string]bool{}
	for i, x := range list {
		// Alltid noen tabeller med kolonner, selv uten treff — ellers står
		// modellen uten noe å skrive SQL mot.
		if i < tableDetailLimit || x.score > 0 {
			out[x.name] = true
		}
	}
	return out
}

// rowHint viser tabellstørrelsen kompakt. Bare STORE tabeller merkes —
// poenget er å advare, ikke å fylle skjemaet med tall.
func rowHint(rows int64) string {
	switch {
	case rows >= 1_000_000:
		return fmt.Sprintf("[~%dM rader: aggreger, aldri fritekstsøk]", rows/1_000_000)
	case rows >= 100_000:
		return fmt.Sprintf("[~%dk rader: filtrer alltid]", rows/1000)
	}
	return ""
}

// shortType kutter SQL-typenavn til det modellen faktisk trenger å vite.
// «timestamp with time zone» er 25 tegn i hver eneste melding; «ts» sier det
// samme for den som skal skrive en WHERE-klausul.
func shortType(t string) string {
	l := strings.ToLower(strings.TrimSpace(t))
	switch {
	case strings.HasPrefix(l, "timestamp"), strings.HasPrefix(l, "datetime"):
		return "ts"
	case strings.HasPrefix(l, "date"):
		return "date"
	case strings.HasPrefix(l, "time"):
		return "time"
	case strings.HasPrefix(l, "character varying"), strings.HasPrefix(l, "varchar"),
		strings.HasPrefix(l, "nvarchar"), strings.HasPrefix(l, "text"),
		strings.HasPrefix(l, "char"):
		return "text"
	case strings.HasPrefix(l, "bigint"), strings.HasPrefix(l, "integer"),
		strings.HasPrefix(l, "smallint"), strings.HasPrefix(l, "int"):
		return "int"
	case strings.HasPrefix(l, "numeric"), strings.HasPrefix(l, "decimal"),
		strings.HasPrefix(l, "double"), strings.HasPrefix(l, "real"),
		strings.HasPrefix(l, "float"), strings.HasPrefix(l, "money"):
		return "num"
	case strings.HasPrefix(l, "bool"), strings.HasPrefix(l, "bit"):
		return "bool"
	case l == "":
		return ""
	}
	return l
}

// buildDBTool bygger query_database-verktøyet for en tenant/bruker. Hvis
// onlyConnID er satt, begrenses verktøyet til den ene tilkoblingen (brukes
// av agent-scheduleren, som kjører uten en HTTP-sesjon).
func (s *Server) buildDBTool(tenantID, userID, onlyConnID string) *dbToolCtx {
	return s.buildDBToolFocused(tenantID, userID, onlyConnID, "")
}

// buildDBToolFocused tar med brukerens melding: på store baser avgjør den
// hvilke tabeller som vises med kolonner (se relevantTables).
func (s *Server) buildDBToolFocused(tenantID, userID, onlyConnID, focus string) *dbToolCtx {
	conns, err := s.store.ListConnections(tenantID)
	if err != nil || len(conns) == 0 {
		return nil
	}

	t := &dbToolCtx{conns: map[string]dbConn{}}
	var schema strings.Builder
	for _, c := range conns {
		// Kolonnene per tabell for denne tilkoblingen — grunnlaget for presise
		// feilmeldinger når modellen bommer på et kolonnenavn.
		colIndex := map[string][]string{}
		textIndex := map[string][]string{}
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
		// Store baser: send kolonnene for de mest relevante tabellene, og bare
		// NAVNET på resten. Med 50 tabeller ville hele skjemaet vært titusener
		// av tegn i hver melding — modellen kan be om resten ved å spørre.
		detailed := relevantTables(tables, focus)
		for _, tb := range tables {
			allowed = append(allowed, tb.Name)
			if detailed != nil && !detailed[tb.Name] {
				// Kun navnet: modellen ser at tabellen finnes.
				fmt.Fprintf(&schema, "%s(…)", tb.Name)
				if n := rowHint(tb.Rows); n != "" {
					schema.WriteString(" " + n)
				}
				if tb.Description != "" {
					fmt.Fprintf(&schema, " — %s", tb.Description)
				}
				schema.WriteString("\n")
				continue
			}
			cols := make([]string, 0, len(tb.ColumnList))
			for _, col := range tb.ColumnList {
				s := col.Name + " " + shortType(col.Type)
				if col.Description != "" {
					s += " (" + col.Description + ")"
				}
				cols = append(cols, s)
			}
			for _, col := range tb.ColumnList {
				lower := strings.ToLower(tb.Name)
				colIndex[lower] = append(colIndex[lower], col.Name)
				if shortType(col.Type) == "text" {
					textIndex[lower] = append(textIndex[lower], col.Name)
				}
			}
			fmt.Fprintf(&schema, "%s(%s)", tb.Name, strings.Join(cols, ", "))
			if n := rowHint(tb.Rows); n != "" {
				schema.WriteString(" " + n)
			}
			if tb.Description != "" {
				fmt.Fprintf(&schema, " — %s", tb.Description)
			}
			schema.WriteString("\n")
		}
		for _, l := range links {
			fmt.Fprintf(&schema, "JOIN: %s.%s = %s.%s\n", l.FromTable, l.FromColumn, l.ToTable, l.ToColumn)
		}
		viewMap := map[string]string{}
		replaced := map[string]bool{}
		for _, v := range views {
			fmt.Fprintf(&schema, "Ferdig spørring %q", v.Name)
			if v.Description != "" {
				fmt.Fprintf(&schema, " (%s)", v.Description)
			}
			fmt.Fprintf(&schema, ": %s — spørres som FROM %s\n", v.SQL, v.Name)
			// SIKKERHET: et utsnitt ERSTATTER tabellen sin — både utsnittsnavnet
			// og selve tabellnavnet ekspanderes til utsnittets SQL server-side
			// (SafeQueryViewsN). Uten dette kunne modellen spørre råtabellen
			// direkte og hente rader utenfor admin-filteret (2023-utsnitt ga
			// 2026-rader).
			sql := strings.TrimSuffix(strings.TrimSpace(v.SQL), ";")
			viewMap[strings.ToLower(v.Name)] = sql
			base := strings.ToLower(strings.TrimSuffix(v.Name, "_query"))
			viewMap[base] = sql
			replaced[base] = true
			allowed = append(allowed, v.Name)
		}
		if len(replaced) > 0 {
			kept := allowed[:0]
			for _, name := range allowed {
				if !replaced[strings.ToLower(name)] {
					kept = append(kept, name)
				}
			}
			allowed = kept
		}
		t.conns[c.ID] = dbConn{conn: c, creds: creds, allowed: allowed, views: viewMap, columns: colIndex, textColumns: textIndex}
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
				"Bruk JOIN-nøklene under ved behov.\n" +
				"Skriv LETTE spørringer — tabellene er store og kjøres mot kundens " +
				"driftsbase: konkrete kolonner (aldri SELECT *), alltid radgrense, " +
				"tidsfilter når det finnes datofelt, og aggreger med SUM/COUNT + " +
				"GROUP BY i stedet for å hente rader. Fritekstsøk: søk i ÉN kolonne, " +
				"og unngå ledende jokertegn ('%tekst%') på store tabeller.\n\n" + schema.String(),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"connection_id": map[string]any{"type": "string", "description": "id for databasen"},
					"sql":           map[string]any{"type": "string", "description": "SELECT-spørringen. Ved ORDER BY: legg ALLTID til en entydig andresortering (f.eks. dokumentnr eller id) så «nyligste/største» aldri er vilkårlig blant like verdier."},
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
// runDBQuery kjører modellens spørring og returnerer et TYPET utfall. Ved bom
// prøver den selv neste vei (nærmeste navn, tabell-lista) før modellen ser
// svaret — se dbstrategy.go for hvorfor.
func (s *Server) runDBQuery(ctx context.Context, t *dbToolCtx, connID, query string) dbAttempt {
	if t == nil {
		return dbAttempt{outcome: dbFailed, sql: query, text: "Ingen database er tilgjengelig."}
	}
	dc, resolvedID, ok, msg := t.resolveConn(connID, query)
	if !ok {
		return dbAttempt{outcome: dbNoAccess, sql: query, text: msg}
	}
	if resolvedID != connID {
		s.log.Info("db-spørring rutet om", "fra", connID, "til", resolvedID)
	}

	db, err := connector.Open(ctx, dc.creds)
	if err != nil {
		s.log.Warn("db-tilkobling feilet", "err", err)
		return dbAttempt{outcome: dbFailed, sql: query, conn: dc.conn.Name,
			text: "Kunne ikke koble til databasen: " + err.Error()}
	}
	defer db.Close()

	cols, rows, err := connector.SafeQueryViewsN(ctx, db, dc.creds.Driver, query, dc.allowed, dc.views, 0)
	if err != nil {
		s.log.Warn("db-spørring feilet", "connection", dc.conn.Name, "driver", dc.creds.Driver,
			"err", err, "sql", query)
		// Gi modellen nok kontekst til å rette seg selv i neste runde.
		msg := "Spørringen feilet: " + err.Error()
		if strings.Contains(err.Error(), "ikke tilgjengelig") {
			msg += "\nDenne databasen (" + dc.conn.Name + ") har: " + strings.Join(dc.allowed, ", ")
		}
		// Konkret retting i stedet for en rå databasefeil (dbstrategy.go).
		if hint := sqlFixHint(err.Error(), query, dc); hint != "" {
			msg += "\n" + hint
		}
		// Timeout betyr nesten alltid at spørringen skannet hele tabellen.
		// Uten konkret veiledning prøver modellen det SAMME på nytt — vi så
		// tre identiske forsøk på rad i prod.
		if strings.Contains(err.Error(), "deadline exceeded") ||
			strings.Contains(err.Error(), "timeout") {
			msg = "Spørringen brukte for lang tid og ble avbrutt. Den skannet " +
				"trolig hele tabellen. Prøv IGJEN med en lettere variant:\n" +
				"- velg konkrete kolonner, aldri SELECT *\n" +
				"- begrens alltid antall rader (TOP 100 / LIMIT 100)\n" +
				"- legg på et tidsfilter når tabellen har datofelt (siste 12 mnd)\n" +
				"- unngå LIKE '%tekst%' over flere kolonner; søk i ÉN kolonne, " +
				"helst med prefiks ('tekst%')\n" +
				"- er tabellen stor, aggreger (SUM/COUNT + GROUP BY) i stedet " +
				"for å hente rader\n" +
				"Klarer du ikke gjøre den lettere, si til brukeren at spørringen " +
				"er for tung — ikke gjenta den samme spørringen."
		}
		outcome := dbFailed
		if strings.Contains(err.Error(), "ikke tilgjengelig") {
			outcome = dbNoAccess
		}
		return dbAttempt{outcome: outcome, sql: query, conn: dc.conn.Name, text: msg}
	}
	s.log.Info("db-spørring", "connection", dc.conn.Name, "rader", len(rows))

	att := dbAttempt{outcome: dbOK, sql: query, conn: dc.conn.Name, cols: cols, rows: rows}
	// Et aggregat uten treff ER tomt, selv om det kommer tilbake som én rad
	// med 0. Klassifiseres det som dbOK, tror hele maskineriet at vi fant noe:
	// dbSucceeded blir sann, kvalitetsporten står ned, og modellen står fritt
	// til å formulere fraværet som et funn («har kjøpt for 0 kroner»).
	if len(rows) == 0 || zeroAggregate(cols, rows) {
		att.outcome = dbEmpty
	}

	// Strategi: bommet et fritekstfilter, slå opp nærmeste verdi FØR modellen
	// rekker å konkludere med at noe «ikke finnes». Ett lett prefikskall.
	if att.outcome == dbEmpty {
		if col, needle, hits := s.nearestValues(ctx, db, dc, query); len(hits) > 0 {
			att.note = fmt.Sprintf("Ingen rader for %q i %s. Nærmeste verdier som FINNES: %s. "+
				"Kjør spørringen på nytt med den riktige, og fortell brukeren hvilken du brukte.",
				needle, col, strings.Join(quoteAll(hits), ", "))
			s.log.Info("db-strategi: nærmeste verdier", "søkte", needle, "fant", strings.Join(hits, ", "))
		}
	}

	// Strategi: sorterte vi på en kolonne som er lik i hele resultatet, er
	// rekkefølgen meningsløs. Det skal sies, ikke pyntes over.
	if att.outcome == dbOK {
		if col, val := constantColumn(cols, rows); col != "" {
			shown := val
			if shown == "" {
				shown = "tom"
			}
			att.note = fmt.Sprintf("MERK: %s er %s i alle radene, så den kolonnen skiller ikke "+
				"radene fra hverandre. Si det til brukeren i stedet for å presentere rekkefølgen "+
				"som en rangering.", col, shown)
			s.log.Info("db-strategi: konstant kolonne", "kolonne", col, "verdi", val)
		}
	}

	out, _ := json.Marshal(map[string]any{"columns": cols, "rows": rows, "row_count": len(rows)})
	att.text = string(out)
	if att.note != "" {
		att.text += "\n\n" + att.note
	}
	return att
}

// quoteAll setter anførselstegn rundt hver verdi så modellen ser dem som
// konkrete strenger å bruke, ikke som løpende tekst.
func quoteAll(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, "\""+v+"\"")
	}
	return out
}
