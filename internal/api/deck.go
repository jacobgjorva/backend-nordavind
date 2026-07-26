package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/deck"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Presentasjonsflyten: modellen velger blant kittets ferdige slide-typer og
// fyller feltene. Den komponerer aldri layout selv, og den bytter aldri hele
// slide-listen — hver endring er en patch på én slide (set_slide), så
// brukerens egne redigeringer i canvaset overlever neste instruks.

const deckSystemBase = "Du bygger EN presentasjon for brukeren. " +
	"IKKE svar med prat eller forklaringer — utfør ved å kalle verktøyene. " +
	"Én slide om gangen: set_slide med op=add legger til en ny slide bakerst, " +
	"op=set endrer felt på en slide som finnes (oppgi id), op=remove sletter en, " +
	"op=move flytter en. set_deck setter tittel, tema og stil på hele presentasjonen. " +
	"Send KUN feltene som skal endres — alt du utelater beholdes som det er. " +
	"Brukeren kan ha rettet tekst selv i canvaset: aldri bygg presentasjonen på nytt " +
	"når du blir bedt om å endre én ting. " +
	"Gjør KUN det brukeren ber om — ikke legg til slides på eget initiativ. " +
	"Grafer og nøkkeltall henter data LIVE hver gang presentasjonen åpnes: " +
	"skriv SQL relativ til CURRENT_DATE, aldri hardkodede datoer, og oppgi " +
	"connection_id fra skjemaet. " +
	"Vil brukeren endre utseendet, bytter du tema eller setter style-tokens " +
	"(bg, text, muted, faint, line, grid, sans, mono, palette) — aldri layout. " +
	"Etter verktøykallene svarer du med maks ett kort ord (f.eks. «Ok»)."

// deckSystem bygger systemteksten: kit-katalogen (generert fra JSON-filene),
// hva som ALLEREDE ligger på lerretet, og databaseskjemaet for SQL-en.
//
// Lerret-tilstanden hentes fra lagret spec, aldri fra chat-historikken:
// instruksene lever ikke i tråden, og brukerens egne rettinger i canvaset
// skal telle like mye som modellens egne.
func (s *Server) deckSystem(ctx context.Context, theme, slug string) string {
	var b strings.Builder
	b.WriteString(deckSystemBase)
	b.WriteString("\n\n")
	b.WriteString(deck.Get(theme).Catalog())
	b.WriteString("\n" + s.deckState(ctx, slug))
	if len(deck.Names()) > 1 {
		b.WriteString("Andre temaer: " + strings.Join(deck.Names(), ", ") + "\n")
	}
	if dbCtx := s.dbToolContext(ctx); dbCtx != nil && dbCtx.schema != "" {
		b.WriteString("\nDatabaseskjema:\n" + dbCtx.schema)
	}
	return b.String()
}

// deckState er lerretet slik det står nå: én linje per slide med id, layout
// og en kort forhåndsvisning av tekstfeltene. Kort med vilje — modellen
// trenger å vite hva som finnes og hvilken id den skal patche, ikke lese hele
// presentasjonen på nytt hver tur.
func (s *Server) deckState(ctx context.Context, slug string) string {
	if strings.TrimSpace(slug) == "" {
		return "Lerretet er tomt: ingen slides ennå."
	}
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return ""
	}
	w, err := s.store.Widget(slug, user.ID)
	if err != nil {
		return ""
	}
	var spec map[string]any
	json.Unmarshal([]byte(w.Spec), &spec)
	slides := slidesOf(spec)
	if len(slides) == 0 {
		return "Lerretet er tomt: ingen slides ennå."
	}
	var b strings.Builder
	b.WriteString("Slidene som ligger på lerretet nå (bruk id-en når du endrer):\n")
	for i, sl := range slides {
		id, _ := sl["id"].(string)
		layout, _ := sl["layout"].(string)
		fmt.Fprintf(&b, "%d. id=%s layout=%s", i+1, id, layout)
		for _, f := range []string{"title", "content"} {
			if v, _ := sl[f].(string); v != "" {
				fmt.Fprintf(&b, " %s=%q", f, preview(v, 60))
			}
		}
		if _, ok := sl["widget"]; ok {
			b.WriteString(" (har graf)")
		}
		if wl, ok := sl["widgets"].([]any); ok {
			fmt.Fprintf(&b, " (har %d visualiseringer)", len(wl))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// deckIsEmpty sier om presentasjonen ennå ikke har en eneste slide.
func (s *Server) deckIsEmpty(ctx context.Context, slug string) bool {
	if strings.TrimSpace(slug) == "" {
		return true
	}
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return true
	}
	w, err := s.store.Widget(slug, user.ID)
	if err != nil {
		return true
	}
	var spec map[string]any
	json.Unmarshal([]byte(w.Spec), &spec)
	return len(slidesOf(spec)) == 0
}

// preview kutter en tekst til en kort én-linjes forhåndsvisning.
func preview(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// deckTheme gir temaet en åpen presentasjon bruker (standardtemaet ellers),
// så katalogen modellen ser er den som faktisk rendres.
func (s *Server) deckTheme(ctx context.Context, slug string) string {
	if strings.TrimSpace(slug) == "" {
		return deck.Default
	}
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return deck.Default
	}
	w, err := s.store.Widget(slug, user.ID)
	if err != nil {
		return deck.Default
	}
	var spec struct {
		Theme string `json:"theme"`
	}
	json.Unmarshal([]byte(w.Spec), &spec)
	return deck.Get(spec.Theme).Name
}

// deckTools er verktøyene i presentasjonsflyten. Layout-nøklene kommer fra
// kittet, så nye slide-typer blir tilgjengelige uten kodeendring.
func deckTools(theme string) []any {
	k := deck.Get(theme)
	slideWidget := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":          map[string]any{"type": "string", "description": "kpi | line | bar | donut | table"},
			"title":         map[string]any{"type": "string"},
			"value":         map[string]any{"type": "string"},
			"unit":          map[string]any{"type": "string"},
			"connection_id": map[string]any{"type": "string"},
			"sql":           map[string]any{"type": "string"},
			"x":             map[string]any{"type": "string", "description": "kategori-/dato-kolonne"},
			"y":             map[string]any{"type": "string", "description": "verdi-kolonne"},
		},
	}
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "set_slide",
				"description": "Legg til, endre, flytt eller slett ÉN slide. Send kun feltene som skal endres — " +
					"resten av sliden beholdes.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"op": map[string]any{
							"type":        "string",
							"description": "add | set | remove | move (standard: add uten id, set med id)",
						},
						"id":     map[string]any{"type": "string", "description": "slide-id ved set/remove/move"},
						"after":  map[string]any{"type": "string", "description": "plasser sliden etter denne id-en (add/move). Tom = bakerst."},
						"layout": map[string]any{"type": "string", "description": strings.Join(k.LayoutKeys(), " | ")},
						"title":  map[string]any{"type": "string"},
						"content": map[string]any{
							"type":        "string",
							"description": "brødtekst eller kulepunkter (markdown)",
						},
						"image": map[string]any{"type": "string", "description": "bildenavn fra temaet, f.eks. bg-1"},
						"images": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
						"widget": slideWidget,
						"widgets": map[string]any{
							"type":  "array",
							"items": slideWidget,
						},
					},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "set_deck",
				"description": "Sett tittel, tema eller stil på hele presentasjonen.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{"type": "string"},
						"theme": map[string]any{"type": "string", "description": strings.Join(deck.Names(), " | ")},
						"style": map[string]any{
							"type":        "object",
							"description": "token-overrides oppå temaet (bg, text, muted, faint, line, grid, sans, mono, palette)",
						},
					},
				},
			},
		},
	}
}

// deckSpec henter widgetens spec som kart, og oppretter presentasjonen først
// om flyten ikke har noen ennå (samme mønster som runWidgetOp).
func (s *Server) deckSpec(user store.User, slug, title string) (string, map[string]any, bool) {
	if strings.TrimSpace(slug) == "" {
		if title == "" {
			title = "Presentasjon"
		}
		ns := slugify(title)
		if ns == "" {
			ns = "presentasjon"
		}
		created, err := s.store.CreateWidget(user.TenantID, user.ID, ns, title)
		if err != nil {
			created, err = s.store.CreateWidget(user.TenantID, user.ID,
				ns+"-"+strings.ToLower(randToken(4)), title)
			if err != nil {
				s.log.Error("kunne ikke opprette presentasjon", "err", err)
				return "", nil, false
			}
		}
		return created.Slug, map[string]any{"type": "deck", "title": title}, true
	}
	w, err := s.store.Widget(slug, user.ID)
	if err != nil {
		return "", nil, false
	}
	var spec map[string]any
	json.Unmarshal([]byte(w.Spec), &spec)
	if spec == nil {
		spec = map[string]any{}
	}
	spec["type"] = "deck"
	if _, ok := spec["title"].(string); !ok && w.Title != "" {
		spec["title"] = w.Title
	}
	return slug, spec, true
}

// saveDeck lagrer specen tilbake på widgeten.
func (s *Server) saveDeck(user store.User, slug string, spec map[string]any) bool {
	title, _ := spec["title"].(string)
	if title == "" {
		title = "Presentasjon"
	}
	out, _ := json.Marshal(spec)
	if err := s.store.SetWidget(slug, user.ID, title, string(out)); err != nil {
		s.log.Error("kunne ikke lagre presentasjon", "err", err)
		return false
	}
	return true
}

// slidesOf henter slide-listen som kart-liste.
func slidesOf(spec map[string]any) []map[string]any {
	raw, _ := spec["slides"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func putSlides(spec map[string]any, slides []map[string]any) {
	arr := make([]any, len(slides))
	for i, s := range slides {
		arr[i] = s
	}
	spec["slides"] = arr
}

// indexOfSlide finner sliden med gitt id (-1 om ukjent).
func indexOfSlide(slides []map[string]any, id string) int {
	for i, s := range slides {
		if sid, _ := s["id"].(string); sid == id && id != "" {
			return i
		}
	}
	return -1
}

// slideFields er feltene modellen kan sette på en slide. Alt annet ignoreres.
var slideFields = []string{"layout", "title", "content", "image", "images", "widget", "widgets"}

// runSlideOp utfører ett set_slide-kall. Returnerer kvitteringen til modellen
// og slugen presentasjonen ligger på.
func (s *Server) runSlideOp(ctx context.Context, slug, rawArgs string) (string, string) {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget.", slug
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "Ugyldige argumenter.", slug
	}
	return s.patchSlide(user, slug, args)
}

// patchSlide er den ENE veien inn til slide-listen: både modellens set_slide
// og brukerens redigering i canvaset går her, med samme patch-semantikk.
func (s *Server) patchSlide(user store.User, slug string, args map[string]any) (string, string) {
	id, _ := args["id"].(string)
	op, _ := args["op"].(string)
	if op == "" {
		if id == "" {
			op = "add"
		} else {
			op = "set"
		}
	}
	newDeck := strings.TrimSpace(slug) == ""
	title, _ := args["title"].(string)
	slug, spec, ok2 := s.deckSpec(user, slug, title)
	if !ok2 {
		return "Fant ikke presentasjonen.", slug
	}
	slides := slidesOf(spec)
	after, _ := args["after"].(string)

	switch op {
	case "remove":
		i := indexOfSlide(slides, id)
		if i < 0 {
			return "Fant ingen slide med id " + id + ".", slug
		}
		slides = append(slides[:i], slides[i+1:]...)
	case "move":
		i := indexOfSlide(slides, id)
		if i < 0 {
			return "Fant ingen slide med id " + id + ".", slug
		}
		sl := slides[i]
		slides = append(slides[:i], slides[i+1:]...)
		slides = insertSlide(slides, sl, after)
	case "add":
		sl := map[string]any{"id": "s" + randToken(6)}
		applySlideFields(sl, args)
		slides = insertSlide(slides, sl, after)
		id = sl["id"].(string)
	default: // set
		i := indexOfSlide(slides, id)
		if i < 0 {
			return "Fant ingen slide med id " + id + ". Bruk op=add for en ny slide.", slug
		}
		applySlideFields(slides[i], args)
	}

	putSlides(spec, slides)
	if !s.saveDeck(user, slug, spec) {
		return "Kunne ikke lagre.", slug
	}
	if newDeck {
		return "Presentasjon opprettet (slug=" + slug + "), slide " + id + " lagt til.", slug
	}
	return "ok (slide " + id + ")", slug
}

// insertSlide setter sliden etter given id, eller bakerst.
func insertSlide(slides []map[string]any, sl map[string]any, after string) []map[string]any {
	if after != "" {
		if i := indexOfSlide(slides, after); i >= 0 {
			out := append([]map[string]any{}, slides[:i+1]...)
			out = append(out, sl)
			return append(out, slides[i+1:]...)
		}
	}
	return append(slides, sl)
}

// applySlideFields patcher feltene som faktisk er med i kallet. Tom streng
// fjerner feltet — det er slik modellen tar bort en bildetekst.
func applySlideFields(sl map[string]any, args map[string]any) {
	for _, f := range slideFields {
		v, ok := args[f]
		if !ok || v == nil {
			continue
		}
		if str, isStr := v.(string); isStr && str == "" {
			delete(sl, f)
			continue
		}
		sl[f] = v
	}
}

// runDeckOp utfører set_deck: tittel, tema og stil på hele presentasjonen.
func (s *Server) runDeckOp(ctx context.Context, slug, rawArgs string) (string, string) {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget.", slug
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "Ugyldige argumenter.", slug
	}
	newDeck := strings.TrimSpace(slug) == ""
	title, _ := args["title"].(string)
	slug, spec, ok2 := s.deckSpec(user, slug, title)
	if !ok2 {
		return "Fant ikke presentasjonen.", slug
	}
	if title != "" {
		spec["title"] = title
	}
	if th, _ := args["theme"].(string); th != "" {
		spec["theme"] = deck.Get(th).Name
	}
	if st, ok := args["style"].(map[string]any); ok && len(st) > 0 {
		cur, _ := spec["style"].(map[string]any)
		if cur == nil {
			cur = map[string]any{}
		}
		for k, v := range st {
			cur[k] = v
		}
		spec["style"] = cur
	}
	if !s.saveDeck(user, slug, spec) {
		return "Kunne ikke lagre.", slug
	}
	if newDeck {
		return "Presentasjon opprettet (slug=" + slug + ").", slug
	}
	return "ok", slug
}

// handleDeckSlide er brukerens egen redigering i canvaset: samme patch som
// modellen gjør, så et dobbeltklikk og en chat-instruks aldri kan komme i
// utakt med hverandre.
func (s *Server) handleDeckSlide(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "ugyldig kropp", http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	if strings.TrimSpace(slug) == "" {
		http.Error(w, "mangler slug", http.StatusBadRequest)
		return
	}
	result, _ := s.patchSlide(user, slug, args)
	if !strings.HasPrefix(result, "ok") && !strings.HasPrefix(result, "Presentasjon") {
		http.Error(w, result, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeckMeta setter tittel, tema eller stil fra canvaset.
func (s *Server) handleDeckMeta(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "ugyldig kropp", http.StatusBadRequest)
		return
	}
	slug, spec, ok2 := s.deckSpec(user, r.PathValue("slug"), "")
	if !ok2 {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if title, _ := args["title"].(string); title != "" {
		spec["title"] = title
	}
	if th, _ := args["theme"].(string); th != "" {
		spec["theme"] = deck.Get(th).Name
	}
	if st, ok := args["style"].(map[string]any); ok {
		spec["style"] = st
	}
	if !s.saveDeck(user, slug, spec) {
		http.Error(w, "kunne ikke lagre", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeckKits gir frontend kittene: tokens, bilder og slide-komposisjoner.
// Samme JSON som modellen får katalogen sin fra — én kilde til sannhet.
func (s *Server) handleDeckKits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.user(w, r); !ok {
		return
	}
	writeJSON(w, map[string]any{"default": deck.Default, "kits": deck.All()})
}

// handleAdhocQuery kjører en lagret SELECT mot en av brukerens tilkoblinger
// (read-only, tabell-tilgang håndheves av SafeQueryN). Brukes av slides som
// bærer sin egen widget-spec og henter live data ved visning.
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
