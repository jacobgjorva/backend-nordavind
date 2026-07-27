package api

import (
	"bytes"
	"context"
	"encoding/json"
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

// brainModel: uttrekk krever at modellen holder styr på entiteter og et
// lukket vokabular samtidig. Det er en tyngre jobb enn den gamle
// «tre løse noder», og kjøres uansett i bakgrunnen — aldri i svarløyfa.
const brainModel = "mistral-medium-3.5-128b"

// ExtractToBrain leser en tekst og lagrer det som holder mål. Returnerer
// antall påstander og prosedyrer som ble lagret.
func (s *Server) ExtractToBrain(ctx context.Context, tenantID, userID, text, sourceKind, sourceID string) (int, int) {
	if strings.TrimSpace(text) == "" {
		return 0, 0
	}
	// Den som snakker MÅ være en kjent entitet. Uten det blir «sjefen min»
	// hengende i løse lufta, og modellen finner på en «Bruker»-person.
	speaker := s.speakerEntity(tenantID, userID)
	prop, err := s.proposeClaims(ctx, tenantID, text, speaker)
	if err != nil {
		s.log.Warn("hjerne-uttrekk feilet", "err", err)
		return 0, 0
	}
	if len(prop.Rejected) > 0 {
		// Avvisningene er signalet om at vokabularet mangler noe folk sier.
		s.log.Info("hjerne-uttrekk: forkastet", "antall", len(prop.Rejected), "eksempel", prop.Rejected[0])
	}
	return s.storeProposal(ctx, tenantID, userID, sourceKind, sourceID, prop)
}

// speakerEntity sørger for at den innloggede brukeren finnes i grafen, og
// gir navnet modellen skal bruke om seg selv («jeg», «meg», «min»).
func (s *Server) speakerEntity(tenantID, userID string) string {
	name := userID
	if u, err := s.store.UserByID(userID); err == nil && u.Email != "" {
		name = u.Email
		if i := strings.Index(name, "@"); i > 0 {
			name = name[:i]
		}
	}
	if name == "" {
		return ""
	}
	if _, err := s.store.UpsertEntity(tenantID,
		store.Entity{Kind: "person", Name: name}, nil); err != nil {
		return ""
	}
	return name
}

// proposeClaims kjører modellkallet og validerer svaret.
func (s *Server) proposeClaims(ctx context.Context, tenantID, text, speaker string) (brain.Proposal, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Kjente entiteter sendes med så modellen bruker EKSISTERENDE navn i
	// stedet for å lage en ny variant av samme person.
	var known []string
	if ents, err := s.store.EntitiesByKind(tenantID, ""); err == nil {
		for _, e := range ents {
			known = append(known, e.Name)
			if len(known) >= 60 {
				break
			}
		}
	}
	userMsg := "Kjente entiteter (bruk disse navnene når det er samme ting): " +
		orNone(strings.Join(known, "; ")) + "\n"
	if speaker != "" {
		userMsg += "Den som skriver heter " + speaker +
			" — «jeg», «meg» og «min» viser til " + speaker + ".\n"
	}
	userMsg += "\nTekst:\n" + text

	payload := map[string]any{
		"model": brainModel,
		"messages": []map[string]string{
			{"role": "system", "content": brain.Prompt()},
			{"role": "user", "content": userMsg},
		},
		"temperature":     0.1,
		"max_tokens":      900,
		"response_format": map[string]any{"type": "json_object"},
	}
	body, _ := json.Marshal(payload)
	req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
	if err != nil {
		return brain.Proposal{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return brain.Proposal{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return brain.Proposal{}, err
	}
	if len(out.Choices) == 0 {
		return brain.Proposal{}, nil
	}
	return brain.ParseProposal([]byte(out.Choices[0].Message.Content))
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(ingen ennå)"
	}
	return s
}

// storeProposal lagrer entiteter, påstander og prosedyrer. Koden eier
// identitet og tidslinje: entiteter slås sammen på navn/alias, og en ny
// påstand lukker den den erstatter.
func (s *Server) storeProposal(ctx context.Context, tenantID, userID, sourceKind, sourceID string, p brain.Proposal) (int, int) {
	ids := map[string]string{} // navn (lowercase) → entitets-id
	resolve := func(name string, kind string) string {
		key := strings.ToLower(strings.TrimSpace(name))
		if id, ok := ids[key]; ok {
			return id
		}
		var vec []float32
		if v, err := s.embedCached(ctx, name); err == nil {
			vec = v
		}
		e, err := s.store.UpsertEntity(tenantID, store.Entity{Kind: kind, Name: name}, vec)
		if err != nil {
			s.log.Warn("kunne ikke lagre entitet", "navn", name, "err", err)
			return ""
		}
		ids[key] = e.ID
		return e.ID
	}

	for _, e := range p.Entities {
		var vec []float32
		if v, err := s.embedCached(ctx, e.Name); err == nil {
			vec = v
		}
		saved, err := s.store.UpsertEntity(tenantID,
			store.Entity{Kind: e.Kind, Name: e.Name, Aliases: e.Aliases}, vec)
		if err != nil {
			s.log.Warn("kunne ikke lagre entitet", "navn", e.Name, "err", err)
			continue
		}
		ids[strings.ToLower(e.Name)] = saved.ID
	}

	now := time.Now()
	claims := 0
	for _, c := range p.Claims {
		subjID := resolve(c.Subject, "person")
		if subjID == "" {
			continue
		}
		claim := store.Claim{
			SubjectID: subjID, Predicate: c.Predicate,
			Qualifiers: c.Qualifiers, Confidence: c.Confidence,
			ValidFrom: now, SourceKind: sourceKind, SourceID: sourceID, StatedBy: userID,
		}
		if c.ObjectIsEntity {
			claim.ObjectID = resolve(c.Object, "person")
			if claim.ObjectID == "" {
				continue
			}
		} else {
			claim.ObjectText = c.Object
		}
		// Valider mot vokabularet én gang til rett før lagring: dette er
		// siste sted vi kan stoppe en påstand vi ikke kan forsvare.
		check := brain.Claim{SubjectID: claim.SubjectID, Predicate: claim.Predicate,
			ObjectID: claim.ObjectID, ObjectText: claim.ObjectText,
			Confidence: claim.Confidence, SourceKind: claim.SourceKind, SourceID: claim.SourceID}
		if err := check.Validate(); err != nil {
			s.log.Info("hjerne: påstand avvist ved lagring", "err", err)
			continue
		}
		// Tidslinjen: erstatter denne noe som gjaldt, lukkes det gamle.
		if !multiValuedPredicate(claim.Predicate) {
			if n, err := s.store.SupersedeClaims(tenantID, claim.SubjectID, claim.Predicate, now); err == nil && n > 0 {
				s.log.Info("hjerne: påstand erstattet", "predikat", claim.Predicate, "lukket", n)
			}
		}
		if _, err := s.store.AddClaim(tenantID, claim); err != nil {
			s.log.Warn("kunne ikke lagre påstand", "err", err)
			continue
		}
		claims++
	}

	procs := 0
	for _, pr := range p.Procedures {
		id, err := s.store.SaveProcedure(tenantID, pr.Name, pr.Trigger, pr.Steps, sourceKind, sourceID)
		if err != nil {
			s.log.Warn("kunne ikke lagre prosedyre", "err", err)
			continue
		}
		procs++
		// Prosedyren blir en entitet også, så den kan kobles til kunder og
		// ansvarlige med vanlige påstander.
		procEnt, err := s.store.UpsertEntity(tenantID,
			store.Entity{Kind: "prosedyre", Name: pr.Name}, nil)
		if err != nil {
			continue
		}
		ids[strings.ToLower(pr.Name)] = procEnt.ID
		for _, owner := range pr.Owners {
			ownerID := resolve(owner, "person")
			if ownerID == "" || ownerID == procEnt.ID {
				continue
			}
			s.store.AddClaim(tenantID, store.Claim{
				SubjectID: ownerID, Predicate: "er ansvarlig for", ObjectID: procEnt.ID,
				Confidence: 0.8, ValidFrom: now, SourceKind: sourceKind,
				SourceID: sourceID, StatedBy: userID,
			})
		}
		_ = id
	}
	if claims > 0 || procs > 0 {
		s.log.Info("hjerne fylt", "påstander", claims, "prosedyrer", procs, "kilde", sourceKind)
	}
	return claims, procs
}

// multiValuedPredicate speiler brain.Supersedes: relasjoner der flere svar
// kan være sanne samtidig skal aldri lukke hverandre.
func multiValuedPredicate(p string) bool {
	switch p {
	case "samarbeider med", "har kunde", "er ansvarlig for", "møter",
		"leverer", "bruker", "gjelder for", "er kontakt for":
		return true
	}
	return false
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

// learnFromExchange lærer av en ferdig utveksling, i bakgrunnen. Terskelen
// er lengde alene: en påstand som «Ola er ansvarlig for Vestland Fisk» har
// ingen av markørene den gamle kunnskaps-gaten ser etter, og er nettopp det
// hjernen skal fange.
func (s *Server) learnFromExchange(ctx context.Context, chatID, question, answer string) {
	if s.cfg.BrainMode != "on" {
		return
	}
	q := strings.TrimSpace(question)
	if len([]rune(q)) < 25 {
		return
	}
	user, ok := ctx.Value(userKey).(store.User)
	if !ok || user.TenantID == "" {
		return
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		bg = context.WithValue(bg, userKey, user)
		// Kilden må alltid peke på noe: uten chat-id brukes brukeren, så en
		// påstand aldri blir stående uten opphav.
		src := chatID
		if src == "" {
			src = "bruker:" + user.ID
		}
		s.ExtractToBrain(bg, user.TenantID, user.ID, q+"\n\n(AI svarte: "+answer+")", "chat", src)
	}()
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
