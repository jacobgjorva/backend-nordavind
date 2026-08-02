package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/brain"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Hjernen koblet på: uttrekk skriver påstander, henting traverserer dem.
//
// Alt her ligger ved siden av dagens kunnskapslag, ikke i stedet for. Lappene
// er fortsatt råteksten som siteres; påstandene er destillatet som kan
// resonneres med. Ryker hjernen, faller alt tilbake til dagens oppførsel.

// brainModel: uttrekket kjører på HVER utveksling som passerer filteret, så
// modellvalget avgjør hva hjernen koster i drift. Den lille klarer jobben
// fordi vokabularet er lukket og koden kaster alt som ikke holder mål —
// modellen skal gjenkjenne, ikke resonnere.
const brainModel = "mistral-small-2603"

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(ingen ennå)"
	}
	return s
}

// handleEntitySearch er @-nevningens oppslag: entiteter som starter med det
// brukeren har skrevet. Snarvei, ikke forutsetning — automatisk gjenkjenning
// er fortsatt hovedveien inn i hjernen.
func (s *Server) handleEntitySearch(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	ents, err := s.store.EntitiesByKind(user.TenantID, "")
	if err != nil {
		writeJSON(w, map[string]any{"entities": []any{}})
		return
	}
	type hit struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	out := []hit{}
	for _, e := range ents {
		if q != "" && !matchesPrefix(e, q) {
			continue
		}
		out = append(out, hit{ID: e.ID, Name: e.Name, Kind: e.Kind})
		if len(out) >= 8 {
			break
		}
	}
	writeJSON(w, map[string]any{"entities": out})
}

// matchesPrefix treffer på navn, navnedel eller alias.
func matchesPrefix(e store.Entity, q string) bool {
	for _, n := range append([]string{e.Name}, e.Aliases...) {
		l := strings.ToLower(n)
		if strings.HasPrefix(l, q) {
			return true
		}
		for _, part := range strings.Fields(l) {
			if strings.HasPrefix(part, q) {
				return true
			}
		}
	}
	return false
}

// mentionRe plukker @-nevninger: «@Ola Berg» eller «@Vestland Fisk».
var mentionRe = regexp.MustCompile(`@([\p{L}][\p{L}0-9.-]*(?: [\p{L}][\p{L}0-9.-]*)?)`)

// mentionedEntities finner entitetene brukeren har pekt ut eksplisitt.
// Returnerer også navnene som IKKE ble funnet — de er kandidater til å bli
// nye entiteter, og et signal om hva folk faktisk snakker om.
func (s *Server) mentionedEntities(tenantID, text string) (ids []string, unknown []string) {
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		e, err := s.store.EntityByName(tenantID, name)
		if err == nil && e.ID != "" {
			ids = append(ids, e.ID)
			continue
		}
		// Prøv første ord: «@Ola» skal treffe «Ola Berg».
		if first := strings.Fields(name)[0]; first != name {
			if e, err := s.store.EntityByName(tenantID, first); err == nil && e.ID != "" {
				ids = append(ids, e.ID)
				continue
			}
		}
		unknown = append(unknown, name)
	}
	return ids, unknown
}

// brainGraph gir hjernen i grafvisningens format. Verdipåstander får ingen
// egen node — de ville fylt lerretet med løse bokser; de vises på entiteten
// de gjelder.
func (s *Server) brainGraph(tenantID string) ([]store.KnowledgeNode, []store.KnowledgeEdge) {
	ents, err := s.store.EntitiesByKind(tenantID, "")
	if err != nil || len(ents) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, e.ID)
	}
	claims, err := s.store.ClaimsAbout(tenantID, ids, false)
	if err != nil {
		return nil, nil
	}
	facts := map[string][]string{}
	var edges []store.KnowledgeEdge
	for _, c := range claims {
		if c.ObjectID != "" {
			edges = append(edges, store.KnowledgeEdge{
				FromID: c.SubjectID, ToID: c.ObjectID, Relation: c.Predicate,
			})
			continue
		}
		line := c.Predicate + " " + c.ObjectText
		if len(c.Qualifiers) > 0 {
			var qs []string
			for k, v := range c.Qualifiers {
				qs = append(qs, k+": "+v)
			}
			sort.Strings(qs)
			line += " (" + strings.Join(qs, ", ") + ")"
		}
		facts[c.SubjectID] = append(facts[c.SubjectID], line)
	}
	nodes := make([]store.KnowledgeNode, 0, len(ents))
	for _, e := range ents {
		summary := e.Kind
		if len(e.Aliases) > 0 {
			summary += " (også: " + strings.Join(e.Aliases, ", ") + ")"
		}
		if f := facts[e.ID]; len(f) > 0 {
			sort.Strings(f)
			summary += " — " + strings.Join(f, "; ")
		}
		nodes = append(nodes, store.KnowledgeNode{
			ID: e.ID, Type: e.Kind, Title: e.Name, Summary: summary,
			Status: "accepted", CreatedAt: e.Created,
		})
	}
	return nodes, edges
}

// brainContext er hjernens bidrag til svaret: entitetene spørsmålet nevner,
// traversert to hopp ut. Tomt når ingenting kjennes igjen — da står dagens
// lappe-henting alene, som før.
func (s *Server) brainContext(ctx context.Context, tenantID, userID, query string) string {
	seeds := s.entitiesInQuery(tenantID, query)
	// Eksplisitte @-nevninger er entydige og vinner over gjetting: de peker
	// på en id, ikke på et navn som kan være skrevet feil.
	if tagged, unknown := s.mentionedEntities(tenantID, query); len(tagged) > 0 || len(unknown) > 0 {
		for _, id := range tagged {
			if !contains(seeds, id) {
				seeds = append(seeds, id)
			}
		}
		if len(unknown) > 0 {
			// Brukeren pekte på noe hjernen ikke kjenner — verdt å vite.
			s.log.Info("hjerne: ukjent nevning", "navn", strings.Join(unknown, ", "))
		}
	}
	// «sjefen min», «kunden vår», «jeg» — personlige spørsmål peker på den
	// som spør, og da er brukerens egen entitet utgangspunktet. Uten dette
	// finner hjernen ingenting med mindre navnet står i spørsmålet.
	if isPersonal(query) {
		if id := s.userEntityID(tenantID, userID); id != "" && !contains(seeds, id) {
			seeds = append(seeds, id)
		}
	}
	if len(seeds) == 0 {
		return ""
	}
	g, err := s.loadGraph(tenantID, seeds)
	if err != nil || len(g.Claims) == 0 {
		return ""
	}
	paths := g.Expand(seeds, 2, time.Now())
	out := g.Render(paths, brainMaxLines)
	if out != "" {
		s.log.Info("hjerne brukt", "entiteter", len(seeds), "påstander", len(g.Claims),
			"tekst", strings.ReplaceAll(out, "\n", " | "))
	}
	return out
}

// isPersonal sier om spørsmålet handler om den som spør.
func isPersonal(q string) bool {
	l := " " + strings.ToLower(q) + " "
	for _, w := range []string{" jeg ", " meg ", " min ", " mitt ", " mine ",
		" vi ", " vår ", " vårt ", " våre ", " oss "} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// userEntityID finner brukerens egen entitet i grafen.
func (s *Server) userEntityID(tenantID, userID string) string {
	u, err := s.store.UserByID(userID)
	if err != nil || u.Email == "" {
		return ""
	}
	name := u.Email
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}
	e, err := s.store.EntityByName(tenantID, name)
	if err != nil {
		return ""
	}
	return e.ID
}

// brainMaxLines: påstander er korte, men konteksten skal fortsatt være stram.
const brainMaxLines = 12

// entitiesInQuery finner kjente entiteter nevnt i spørsmålet. Ren
// navne-/aliasmatch — ingen modellkall, ingen latens.
func (s *Server) entitiesInQuery(tenantID, query string) []string {
	ents, err := s.store.EntitiesByKind(tenantID, "")
	if err != nil || len(ents) == 0 {
		return nil
	}
	q := strings.ToLower(query)
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9' ||
			r == 'æ' || r == 'ø' || r == 'å')
	}) {
		words[w] = true
	}
	var ids []string
	for _, e := range ents {
		if matchesEntity(q, words, e.Name) {
			ids = append(ids, e.ID)
			continue
		}
		for _, a := range e.Aliases {
			if matchesEntity(q, words, a) {
				ids = append(ids, e.ID)
				break
			}
		}
	}
	return ids
}

// matchesEntity treffer både hele navnet og et enkeltnavn: folk skriver
// «Ola», ikke «Ola Berg». Delene må stå som EGNE ord i spørsmålet, ellers
// ville «Nes» truffet «nesten».
func matchesEntity(query string, words map[string]bool, name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || len([]rune(n)) < 3 {
		return false
	}
	if strings.Contains(query, n) {
		return true
	}
	for _, p := range strings.Fields(n) {
		// Bare distinkte deler: korte ord som «as» og «og» treffer alt.
		if len([]rune(p)) < 4 {
			continue
		}
		if words[p] {
			return true
		}
		// Norsk bøyer navn: «Vestlands Fisk» skal treffe «Vestland Fisk».
		for w := range words {
			if len([]rune(w)) >= 4 && (strings.HasPrefix(w, p) || strings.HasPrefix(p, w)) {
				return true
			}
		}
	}
	return false
}

// loadGraph henter utsnittet traverseringen trenger: påstandene rundt
// startentitetene, og entitetene de peker på.
func (s *Server) loadGraph(tenantID string, seeds []string) (brain.Graph, error) {
	claims, err := s.store.ClaimsAbout(tenantID, seeds, false)
	if err != nil {
		return brain.Graph{}, err
	}
	// Ett hopp til: påstandene rundt naboene, så to-hopps-slutninger virker.
	next := map[string]bool{}
	for _, c := range claims {
		for _, id := range []string{c.SubjectID, c.ObjectID} {
			if id != "" && !contains(seeds, id) {
				next[id] = true
			}
		}
	}
	if len(next) > 0 {
		var ids []string
		for id := range next {
			ids = append(ids, id)
		}
		more, err := s.store.ClaimsAbout(tenantID, ids, false)
		if err == nil {
			seen := map[string]bool{}
			for _, c := range claims {
				seen[c.ID] = true
			}
			for _, c := range more {
				if !seen[c.ID] {
					claims = append(claims, c)
				}
			}
		}
	}

	g := brain.Graph{Entities: map[string]brain.Entity{}}
	all, err := s.store.EntitiesByKind(tenantID, "")
	if err != nil {
		return g, err
	}
	for _, e := range all {
		g.Entities[e.ID] = brain.Entity{ID: e.ID, Kind: e.Kind, Name: e.Name, Aliases: e.Aliases}
	}
	for _, c := range claims {
		g.Claims = append(g.Claims, brain.Claim{
			ID: c.ID, SubjectID: c.SubjectID, Predicate: c.Predicate,
			ObjectID: c.ObjectID, ObjectText: c.ObjectText, Qualifiers: c.Qualifiers,
			Confidence: c.Confidence, ValidFrom: c.ValidFrom, ValidTo: c.ValidTo,
			SourceKind: c.SourceKind, SourceID: c.SourceID, StatedBy: c.StatedBy,
		})
	}
	return g, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
