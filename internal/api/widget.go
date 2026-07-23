package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// widgetSystemBase gjør modellen til en ren utfører — ingen prat, kun ett
// verktøykall som setter feltene på den ene widgeten.
const widgetSystemBase = "Du bygger ÉN widget for brukeren — én enkelt visualisering. " +
	"IKKE svar med prat eller forklaringer. Utfør KUN det brukeren ber om ved å kalle " +
	"verktøyet set_widget med feltene som skal endres. ALDRI skriv mal-uttrykk som " +
	"{{query_database(...)}} eller kode i noe felt — bruk kun de definerte feltene. " +
	"Widget-typer (velg én):" +
	"\n- kpi: et nøkkeltall. Statisk: sett value (+ unit, delta som «+12%» eller «-3%»). Eller fra " +
	"databasen: sett connection_id + sql (en SELECT som gir ETT tall), la value stå tom." +
	"\n- sparkline: et nøkkeltall med trend. Felt: title, connection_id, sql (SELECT som gir en serie), " +
	"y (verdi-kolonne), evt. x. Siste rad blir tallet, hele serien tegnes som mini-graf." +
	"\n- line: linjediagram over tid. Felt: title, connection_id, sql, x (dato/kategori), y (verdi)." +
	"\n- bar: stolpediagram. Felt: title, connection_id, sql, x (kategori-kolonne), y (verdi-kolonne)." +
	"\n- donut: andel/fordeling. Felt: title, connection_id, sql (SELECT kategori, verdi), x (kategori), y (verdi)." +
	"\n- table: en tabell fra databasen. Felt: title, connection_id, sql (SELECT)." +
	"\n- text: en tekstblokk/overskrift. Felt: content (markdown)." +
	"\nFor data-typene: skriv en SELECT som gir de riktige kolonnene, og oppgi connection_id fra " +
	"skjemaet under. For line/bar/donut/sparkline: oppgi ALLTID både x (kategori/dato) og y (verdi) — " +
	"SELECT må returnere begge kolonnene." +
	"\n\nInteraktive kontroller (valgfritt — virker klient-side på de hentede radene, endrer ikke SQL-en). " +
	"Gjelder ALLE typer, også grafer (bar/line/donut), ikke bare table. Tolk selv hva som gir mening og legg det på. " +
	"filters: kategoriske kolonner med få distinkte verdier (status, type, region) — aldri fritekst, id eller rene tall. " +
	"search: kolonner det er naturlig å lete i (navn, e-post, ordrenr). " +
	"sort: relevante sorteringer med lesbar etikett («Nyeste først», «Høyest beløp»), første er standard. " +
	"group (bar/line/donut): en kolonne det er meningsfullt å summere y per. " +
	"VIKTIG for grafer: en kontroll kan bare virke hvis kolonnen faktisk finnes i radene. Skal en graf " +
	"kunne filtreres på f.eks. region, MÅ SELECT ta med region-kolonnen i tillegg til x og y (og typisk " +
	"gruppere på x + region), slik at brukeren kan skru serien ned til én region. Uten kolonnen i resultatet " +
	"blir det ingen filter-verdier. " +
	"Legg kun på det som faktisk hjelper; en ren tidsserie eller kpi trenger ofte ingen kontroller." +
	"\nEtter verktøykallet svarer du med maks ett kort ord (f.eks. «Ok»). Aldri lange svar."

// widgetSystem legger til databaseskjemaet så modellen kan skrive SQL.
func (s *Server) widgetSystem(ctx context.Context) string {
	if dbCtx := s.dbToolContext(ctx); dbCtx != nil && dbCtx.schema != "" {
		return widgetSystemBase + "\n\nDatabaseskjema:\n" + dbCtx.schema
	}
	return widgetSystemBase
}

// widgetTools er det ene verktøyet modellen bruker i widget-editoren.
func widgetTools() []any {
	compProps := map[string]any{
		"type":          map[string]any{"type": "string", "description": "kpi | sparkline | line | bar | donut | table | text"},
		"title":         map[string]any{"type": "string"},
		"value":         map[string]any{"type": "string"},
		"unit":          map[string]any{"type": "string"},
		"delta":         map[string]any{"type": "string"},
		"content":       map[string]any{"type": "string"},
		"connection_id": map[string]any{"type": "string"},
		"sql":           map[string]any{"type": "string", "description": "SELECT-spørring for table/bar/line"},
		"x":             map[string]any{"type": "string", "description": "kategori-kolonne (bar/line)"},
		"y":             map[string]any{"type": "string", "description": "verdi-kolonne (bar/line)"},
		"search": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "kolonner brukeren kan fritekstsøke i. Tom = ingen søk.",
		},
		"filters": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"column": map[string]any{"type": "string", "description": "kolonne det filtreres på"},
					"label":  map[string]any{"type": "string", "description": "kort etikett i UI"},
				},
				"required": []string{"column"},
			},
			"description": "kolonner som får et filter-nedtrekk med distinkte verdier. Velg kategoriske kolonner (status, type, region), aldri fritekst/id/tall.",
		},
		"sort": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"column": map[string]any{"type": "string", "description": "kolonne det sorteres på"},
					"label":  map[string]any{"type": "string", "description": "kort etikett i UI, f.eks. «Nyeste først»"},
					"dir":    map[string]any{"type": "string", "description": "asc | desc"},
				},
				"required": []string{"column"},
			},
			"description": "valgbare sorteringer. Første element er standard.",
		},
		"group": map[string]any{
			"type":        "string",
			"description": "gruppe-kolonne som aggregerer y som sum per verdi (bar/line/donut). Brukeren kan slå den av.",
		},
	}
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "set_widget",
				"description": "Sett/endre feltene på widgeten. Send feltene som skal endres.",
				"parameters": map[string]any{
					"type": "object", "properties": compProps,
				},
			},
		},
	}
}

// runWidgetOp bruker set_widget-argumentene på widgetens spec og lagrer.
func (s *Server) runWidgetOp(ctx context.Context, slug, rawArgs string) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	w, err := s.store.Widget(slug, user.ID)
	if err != nil {
		return "Fant ikke widgeten."
	}
	var comp map[string]any
	json.Unmarshal([]byte(w.Spec), &comp)
	if comp == nil {
		comp = map[string]any{}
	}
	var args map[string]any
	json.Unmarshal([]byte(rawArgs), &args)
	for k, v := range args {
		if v != nil && v != "" {
			comp[k] = v
		}
	}

	// Behold registertittelen om komponenten ikke selv har en.
	title, _ := comp["title"].(string)
	if title == "" {
		title = w.Title
	}
	out, _ := json.Marshal(comp)
	if err := s.store.SetWidget(slug, user.ID, title, string(out)); err != nil {
		return "Kunne ikke lagre."
	}
	return "ok"
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// slugify gjør en tittel til en trygg slug for /<slug>-kall.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// handleCreateWidget oppretter en tom widget med gitt navn/slug.
func (s *Server) handleCreateWidget(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	slug := slugify(req.Slug)
	if slug == "" {
		slug = slugify(req.Title)
	}
	if slug == "" {
		http.Error(w, "mangler navn", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = slug
	}
	wg, err := s.store.CreateWidget(user.TenantID, user.ID, slug, title)
	if err != nil {
		http.Error(w, "kunne ikke opprette (navnet er kanskje tatt)", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wg)
}

// handleListWidgets returnerer brukerens widgets (til slash-menyen).
func (s *Server) handleListWidgets(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	list, err := s.store.ListWidgets(user.ID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []store.Widget{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// handleGetWidget returnerer én widget med spec.
func (s *Server) handleGetWidget(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	wg, err := s.store.Widget(r.PathValue("slug"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wg)
}

// handleDeleteWidget fjerner en widget brukeren eier.
func (s *Server) handleDeleteWidget(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteWidget(r.PathValue("slug"), user.ID); errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWidgetQuery kjører widgetens datakilde (read-only) og returnerer
// kolonner + rader.
func (s *Server) handleWidgetQuery(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	wg, err := s.store.Widget(r.PathValue("slug"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	var comp map[string]any
	json.Unmarshal([]byte(wg.Spec), &comp)
	conn, _ := comp["connection_id"].(string)
	sql, _ := comp["sql"].(string)
	if sql == "" {
		http.Error(w, "widgeten har ingen spørring", http.StatusBadRequest)
		return
	}
	// Lettvekts-sti: en lagret widget har en fast connection_id, så vi bygger
	// KUN den ene tilkoblingen (ingen dekryptering/skjemabygging for alle) og
	// kjører den lagrede spørringen direkte — ingen LLM, riktig statuskode.
	dbCtx := s.buildDBTool(user.TenantID, user.ID, conn)
	if dbCtx == nil {
		http.Error(w, "databasen er ikke tilgjengelig", http.StatusBadGateway)
		return
	}
	dc, _, ok, _ := dbCtx.resolveConn(conn, sql)
	if !ok {
		http.Error(w, "ukjent tilkobling", http.StatusBadGateway)
		return
	}
	db, err := connector.Open(r.Context(), dc.creds)
	if err != nil {
		http.Error(w, "kunne ikke koble til databasen", http.StatusBadGateway)
		return
	}
	defer db.Close()
	cols, rows, err := connector.SafeQuery(r.Context(), db, dc.creds.Driver, sql, dc.allowed)
	if err != nil {
		http.Error(w, "spørringen feilet: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"columns": cols, "rows": rows, "row_count": len(rows)})
}
