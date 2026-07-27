package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/design"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Verktøylaget mot designmotoren. To moduser, valgt av KODEN ut fra om
// dokumentet har flater fra før:
//
//	tomt      → compose: hele dokumentet i ETT kall, én modelltur
//	har innhold → patch/restyle: bare det brukeren ber om
//
// Den kjeden av ett-kall-per-slide som deck-flyten hadde er borte: den
// betalte for hele historikken på nytt for hver eneste slide.

const designSystemBase = "Du lager et dokument på et designlerret. " +
	"IKKE svar med prat — utfør ved å kalle verktøyet. " +
	"Du velger blant ferdige flate-typer og fyller feltene deres; " +
	"du bestemmer aldri farger, plassering eller typografi. " +
	"«!» betyr påkrevd felt. Hold tekstene korte — de skal leses på avstand."

const designSystemCompose = "\nBygg HELE dokumentet i ETT compose-kall: " +
	"en liste med flater i endelig rekkefølge. Start med en åpningsflate, " +
	"bygg innholdet, avslutt med et poeng eller en oppsummering. " +
	"Sikt på 6-10 flater og varier typene — samme type på rad blir kjedelig. " +
	"Tenk gjennom rekkefølgen FØR du kaller: du får bare ett forsøk på utkastet. " +
	"Skal en flate vise tall fra databasen, sett bare type og tittel på " +
	"visualiseringen — spørringen får du fylle etterpå, når du har sett skjemaet."

const designSystemEdit = "\nDokumentet finnes allerede — gjør KUN det brukeren ber om nå. " +
	"patch endrer én flate (oppgi id); felt du ikke nevner står urørt, og tom " +
	"streng fjerner et felt. action=add/remove/move endrer selve rekkefølgen. " +
	"restyle bytter kitt eller stil-tokens. Brukeren kan ha rettet tekst selv " +
	"på lerretet: bygg aldri dokumentet på nytt for å endre én ting."

// designSystem er systemteksten for turen. Katalogen sendes KUN ved nybygg;
// ved redigering holder tilstanden og den aktuelle layouten.
func (s *Server) designSystem(ctx context.Context, kit design.Kit, doc design.Doc) string {
	var b strings.Builder
	b.WriteString(designSystemBase)
	if len(doc.Surfaces) == 0 {
		b.WriteString(designSystemCompose + "\n\n" + kit.Catalog())
	} else {
		b.WriteString(designSystemEdit + "\n\n" + kit.Catalog() + "\n" + doc.State())
	}
	if names := design.Names(); len(names) > 1 {
		b.WriteString("Andre kitt: " + strings.Join(names, ", ") + "\n")
	}
	// Databaseskjemaet er det dyreste vi kan sende (tusenvis av tokens) og
	// følger derfor bare med når dokumentet FAKTISK har en flate som venter på
	// en spørring. Ved nybygg lager modellen graf-flatene uten SQL, og koden
	// ber om spørringene i én fokusert runde etterpå (dataFollowup) — da
	// betaler en ren tekstpresentasjon aldri for skjemaet.
	if len(doc.NeedsSQL()) > 0 {
		b.WriteString(s.dbSchemaBlock(ctx))
	}
	return b.String()
}

// dbSchemaBlock er databaseskjemaet slik designflyten ser det.
func (s *Server) dbSchemaBlock(ctx context.Context) string {
	dbCtx := s.dbToolContext(ctx)
	if dbCtx == nil || dbCtx.schema == "" {
		return ""
	}
	return "\nDatabaseskjema (skriv SQL relativ til CURRENT_DATE, aldri " +
		"hardkodede datoer, og oppgi connection_id):\n" + dbCtx.schema
}

// dataFollowup er runden som fyller spørringene: den kommer KUN når modellen
// har laget flater med graf eller nøkkeltall, og bærer skjemaet alene — ikke
// katalogen, ikke resten av dokumentet.
func (s *Server) dataFollowup(ctx context.Context, slug string) (string, bool) {
	doc, ok := s.loadDoc(ctx, slug)
	if !ok {
		return "", false
	}
	pending := doc.NeedsSQL()
	if len(pending) == 0 {
		return "", false
	}
	schema := s.dbSchemaBlock(ctx)
	if schema == "" {
		return "", false
	}
	var b strings.Builder
	b.WriteString("Disse flatene mangler spørring:\n")
	for _, sf := range pending {
		fmt.Fprintf(&b, "- %s (%s)\n", sf.ID, sf.Layout)
	}
	b.WriteString("Fyll dem ALLE i ETT patch-kall: bruk ops med én oppføring " +
		"per flate, og sett connection_id, sql og x/y der grafen trenger det. " +
		"Ikke rør noe annet.\n")
	b.WriteString(schema)
	return b.String(), true
}

// surfaceProps er feltene en flate kan ha. Verdiene valideres mot kittets
// slots i motoren — schemaet her holdes bevisst løst og lite.
//
// withSQL styrer om visualiseringene kan bære spørring: ved compose kan de
// IKKE. Modellen har ikke sett databasen på det tidspunktet, og fikk den
// lov, diktet den opp både tabellnavn og connection_id. Spørringene fylles
// i data-runden, der skjemaet faktisk ligger ved.
func surfaceProps(withSQL bool) map[string]any {
	visual := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":  map[string]any{"type": "string", "description": "kpi | line | bar | donut | table"},
			"title": map[string]any{"type": "string"},
			"unit":  map[string]any{"type": "string"},
		},
	}
	if withSQL {
		vp := visual["properties"].(map[string]any)
		vp["connection_id"] = map[string]any{"type": "string"}
		vp["sql"] = map[string]any{"type": "string"}
		vp["x"] = map[string]any{"type": "string", "description": "kategori-/dato-kolonne"}
		vp["y"] = map[string]any{"type": "string", "description": "verdi-kolonne"}
	}
	return map[string]any{
		"title":   map[string]any{"type": "string"},
		"content": map[string]any{"type": "string", "description": "tekst eller kulepunkter (markdown)"},
		"image":   map[string]any{"type": "string", "description": "bildenavn fra kittet, f.eks. bg-1"},
		"images":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"widget":  visual,
		"widgets": map[string]any{"type": "array", "items": visual},
	}
}

// designTools er de tre verktøyene. Layout-nøklene kommer fra kittet, så nye
// flate-typer blir tilgjengelige uten kodeendring.
func designTools(kit design.Kit) []any {
	surface := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"layout": map[string]any{
				"type":        "string",
				"description": strings.Join(kit.LayoutKeys(), " | "),
			},
			"fields": map[string]any{"type": "object", "properties": surfaceProps(false)},
		},
		"required": []string{"layout", "fields"},
	}
	op := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "description": "set | add | remove | move (standard: set)"},
			"id":     map[string]any{"type": "string", "description": "flate-id fra listen"},
			"after":  map[string]any{"type": "string", "description": "plasser etter denne id-en (add/move)"},
			"layout": map[string]any{"type": "string", "description": strings.Join(kit.LayoutKeys(), " | ")},
			"fields": map[string]any{"type": "object", "properties": surfaceProps(true)},
		},
	}
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "compose",
				"description": "Bygg hele dokumentet i ett kall: alle flater, i endelig rekkefølge. " +
					"Erstatter det som eventuelt står der fra før.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":    map[string]any{"type": "string", "description": "dokumentets navn"},
						"surfaces": map[string]any{"type": "array", "items": surface},
					},
					"required": []string{"surfaces"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "patch",
				"description": "Endre flater. Send kun feltene som skal endres — resten står urørt. " +
					"Skal du endre flere flater, send dem ALLE i ops i dette ene kallet.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"ops": map[string]any{
							"type":        "array",
							"items":       op,
							"description": "flere endringer i én operasjon",
						},
						"action": op["properties"].(map[string]any)["action"],
						"id":     op["properties"].(map[string]any)["id"],
						"after":  op["properties"].(map[string]any)["after"],
						"layout": op["properties"].(map[string]any)["layout"],
						"fields": op["properties"].(map[string]any)["fields"],
					},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "restyle",
				"description": "Bytt kitt eller stil-tokens. Innholdet beholdes.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kit":   map[string]any{"type": "string", "description": strings.Join(design.Names(), " | ")},
						"title": map[string]any{"type": "string"},
						"style": map[string]any{
							"type":        "object",
							"description": "token-overrides: bg, text, muted, faint, line, grid, sans, mono, palette",
						},
					},
				},
			},
		},
	}
}

// loadDoc henter dokumentet bak en slug. Gamle presentasjoner leses uendret
// (design.Parse), så ingenting må migreres i databasen.
func (s *Server) loadDoc(ctx context.Context, slug string) (design.Doc, bool) {
	if strings.TrimSpace(slug) == "" {
		return design.Doc{}, false
	}
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return design.Doc{}, false
	}
	return s.docFor(user, slug)
}

func (s *Server) docFor(user store.User, slug string) (design.Doc, bool) {
	w, err := s.store.Widget(slug, user.ID)
	if err != nil {
		return design.Doc{}, false
	}
	doc, ok := design.Parse([]byte(w.Spec))
	if !ok {
		doc = design.Doc{Type: "design", Kind: "deck", Kit: design.Default}
	}
	if doc.Title == "" {
		doc.Title = w.Title
	}
	return doc, true
}

// docKit gir kittet dokumentet bruker (standardkittet for et tomt lerret).
func (s *Server) docKit(ctx context.Context, slug string) (design.Kit, design.Doc) {
	doc, ok := s.loadDoc(ctx, slug)
	if !ok {
		return design.Get(design.Default), design.Doc{}
	}
	return design.Get(doc.Kit), doc
}

// saveDoc lagrer dokumentet på widgeten (samme lager som før).
func (s *Server) saveDoc(user store.User, slug string, doc design.Doc) bool {
	title := doc.Title
	if title == "" {
		title = "Dokument"
	}
	raw, _ := json.Marshal(doc)
	if err := s.store.SetWidget(slug, user.ID, title, string(raw)); err != nil {
		s.log.Error("kunne ikke lagre dokument", "err", err)
		return false
	}
	return true
}

// runCompose bygger hele dokumentet fra ett verktøykall.
func (s *Server) runCompose(ctx context.Context, slug, rawArgs string) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	var args struct {
		Title    string                `json:"title"`
		Surfaces []design.SurfaceInput `json:"surfaces"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "Ugyldige argumenter: " + err.Error()
	}
	if len(args.Surfaces) == 0 {
		return "compose krever minst én flate."
	}
	kit, prev := s.docKit(ctx, slug)
	doc, rep := design.Compose(kit, args.Surfaces)
	doc.Title = firstNonEmpty(args.Title, prev.Title)
	doc.Style = prev.Style
	if !rep.OK {
		return "Ingen flater ble laget. " + rep.String()
	}
	if !s.saveDoc(user, slug, doc) {
		return "Kunne ikke lagre."
	}
	return rep.String()
}

// runDesignPatch utfører én redigering.
func (s *Server) runDesignPatch(ctx context.Context, slug, rawArgs string) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	ops, err := parseOps([]byte(rawArgs))
	if err != nil {
		return "Ugyldige argumenter: " + err.Error()
	}
	if len(ops) == 0 {
		return "patch krever minst én endring."
	}
	kit, doc := s.docKit(ctx, slug)
	var all design.Report
	for _, op := range ops {
		rep := design.Apply(kit, &doc, op)
		all.Applied += rep.Applied
		all.Problems = append(all.Problems, rep.Problems...)
	}
	all.OK = all.Applied > 0
	if !all.OK {
		return all.String()
	}
	if !s.saveDoc(user, slug, doc) {
		return "Kunne ikke lagre."
	}
	return all.String()
}

// parseOps leser både «flere endringer i ops» og en enkelt endring på
// toppnivå — modellen skal aldri kunne bomme på formen.
func parseOps(raw []byte) ([]design.Op, error) {
	var batch struct {
		Ops []design.Op `json:"ops"`
	}
	if err := json.Unmarshal(raw, &batch); err == nil && len(batch.Ops) > 0 {
		return batch.Ops, nil
	}
	var one design.Op
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, err
	}
	if one.ID == "" && one.Action == "" && len(one.Fields) == 0 {
		return nil, nil
	}
	return []design.Op{one}, nil
}

// runRestyle bytter kitt, tittel eller stil-tokens.
func (s *Server) runRestyle(ctx context.Context, slug, rawArgs string) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	var args struct {
		Kit   string            `json:"kit"`
		Title string            `json:"title"`
		Style map[string]string `json:"style"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "Ugyldige argumenter: " + err.Error()
	}
	kit, doc := s.docKit(ctx, slug)
	rep := design.Report{OK: true}
	if args.Kit != "" && args.Kit != doc.Kit {
		rep = design.Restyle(kit, design.Get(args.Kit), &doc)
	}
	if args.Title != "" {
		doc.Title = args.Title
	}
	if len(args.Style) > 0 {
		if doc.Style == nil {
			doc.Style = map[string]string{}
		}
		for k, v := range args.Style {
			doc.Style[k] = v
		}
	}
	if !s.saveDoc(user, slug, doc) {
		return "Kunne ikke lagre."
	}
	if len(rep.Problems) > 0 {
		return "ok. " + rep.String()
	}
	return "ok"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// handleDesignKits gir galleriet: alle kitt med tokens, bilder og layouts.
func (s *Server) handleDesignKits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.user(w, r); !ok {
		return
	}
	writeJSON(w, map[string]any{"default": design.Default, "kits": design.All()})
}

// handleDesignPatch er brukerens egen redigering på lerretet: samme motor og
// samme semantikk som modellens patch, så de to aldri kommer i utakt.
func (s *Server) handleDesignPatch(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "ugyldig kropp", http.StatusBadRequest)
		return
	}
	ops, err := parseOps(raw)
	if err != nil || len(ops) == 0 {
		http.Error(w, "ugyldig kropp", http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	doc, found := s.docFor(user, slug)
	if !found {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	kit := design.Get(doc.Kit)
	var rep design.Report
	for _, op := range ops {
		r1 := design.Apply(kit, &doc, op)
		rep.Applied += r1.Applied
		rep.Problems = append(rep.Problems, r1.Problems...)
	}
	rep.OK = rep.Applied > 0
	if !rep.OK {
		http.Error(w, rep.String(), http.StatusBadRequest)
		return
	}
	if !s.saveDoc(user, slug, doc) {
		http.Error(w, "kunne ikke lagre", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdhocQuery kjører en lagret SELECT mot en av brukerens tilkoblinger
// (read-only, tabell-tilgang håndheves av SafeQueryN). Flater med egen
// widget-spec henter live data denne veien ved visning.
func (s *Server) handleAdhocQuery(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.ConnectionID == "" || req.SQL == "" {
		http.Error(w, "mangler connection_id eller sql", http.StatusBadRequest)
		return
	}
	dbCtx := s.buildDBTool(user.TenantID, user.ID, req.ConnectionID)
	if dbCtx == nil {
		http.Error(w, "databasen er ikke tilgjengelig", http.StatusBadGateway)
		return
	}
	dc, _, ok2, _ := dbCtx.resolveConn(req.ConnectionID, req.SQL)
	if !ok2 {
		http.Error(w, "ukjent tilkobling", http.StatusBadGateway)
		return
	}
	db, err := connector.Open(r.Context(), dc.creds)
	if err != nil {
		http.Error(w, "kunne ikke koble til databasen", http.StatusBadGateway)
		return
	}
	defer db.Close()
	cols, rows, err := connector.SafeQueryN(r.Context(), db, dc.creds.Driver, req.SQL, dc.allowed, 5000)
	if err != nil {
		http.Error(w, "spørringen feilet: "+err.Error(), http.StatusBadGateway)
		return
	}
	if cols == nil {
		cols = []string{}
	}
	if rows == nil {
		rows = [][]string{}
	}
	writeJSON(w, map[string]any{"columns": cols, "rows": rows, "row_count": len(rows)})
}
