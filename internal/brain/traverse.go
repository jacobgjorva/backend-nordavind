package brain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Traverseringen er stedet der hjernen faktisk slutter noe: den går fra
// entitetene i spørsmålet, ut i påstandene rundt dem, og setter sammen fakta
// som aldri har stått i samme setning.
//
// Alt her er ren datamanipulasjon uten modellkall. Modellen formulerer
// svaret; koden bestemmer HVA den får se.

// Graph er utsnittet traverseringen jobber på — hentet fra lageret én gang.
type Graph struct {
	Entities map[string]Entity
	Claims   []Claim
}

// Path er én sti fram til et faktum: påstandene som ble fulgt, i rekkefølge.
// Den er svaret OG begrunnelsen — uten stien kan ingen etterprøve slutningen.
type Path struct {
	Claims []Claim
	Hops   int
}

// Neighbors gir påstandene som rører en entitet, i begge retninger.
func (g Graph) Neighbors(id string, at time.Time) []Claim {
	var out []Claim
	for _, c := range g.Claims {
		if !c.Active(at) {
			continue
		}
		if c.SubjectID == id || c.ObjectID == id {
			out = append(out, c)
		}
	}
	return out
}

// Expand går bredde-først fra startentitetene og samler påstandene innenfor
// maxHops. Resultatet er sortert: nærmest først, deretter høyest sikkerhet —
// det som er mest sannsynlig relevant havner øverst i konteksten.
func (g Graph) Expand(start []string, maxHops int, at time.Time) []Path {
	seenEntity := map[string]bool{}
	seenClaim := map[string]bool{}
	var paths []Path

	frontier := make([]struct {
		id   string
		path []Claim
	}, 0, len(start))
	for _, id := range start {
		if id == "" || seenEntity[id] {
			continue
		}
		seenEntity[id] = true
		frontier = append(frontier, struct {
			id   string
			path []Claim
		}{id, nil})
	}

	for hop := 1; hop <= maxHops && len(frontier) > 0; hop++ {
		var next []struct {
			id   string
			path []Claim
		}
		for _, f := range frontier {
			for _, c := range g.Neighbors(f.id, at) {
				if seenClaim[c.ID] {
					continue
				}
				seenClaim[c.ID] = true
				path := append(append([]Claim{}, f.path...), c)
				paths = append(paths, Path{Claims: path, Hops: hop})
				// Fortsett gjennom entitets-objekter; verdier er blindveier.
				other := c.ObjectID
				if other == f.id {
					other = c.SubjectID
				}
				if other != "" && !seenEntity[other] {
					seenEntity[other] = true
					next = append(next, struct {
						id   string
						path []Claim
					}{other, path})
				}
			}
		}
		frontier = next
	}

	sort.SliceStable(paths, func(i, j int) bool {
		if paths[i].Hops != paths[j].Hops {
			return paths[i].Hops < paths[j].Hops
		}
		return last(paths[i].Claims).Confidence > last(paths[j].Claims).Confidence
	})
	return paths
}

func last(cs []Claim) Claim {
	if len(cs) == 0 {
		return Claim{}
	}
	return cs[len(cs)-1]
}

// Resolve følger en relasjon fra en entitet og gir objektene. «Hvem er sjefen
// min?» er Resolve(jacob, "har sjef") — og med inverse-feltet i vokabularet
// virker det uansett hvilken vei påstanden ble lagret.
func (g Graph) Resolve(from, predicate string, at time.Time) []Entity {
	p, ok := Lookup(predicate)
	if !ok {
		return nil
	}
	var out []Entity
	for _, c := range g.Claims {
		if !c.Active(at) {
			continue
		}
		switch {
		case c.SubjectID == from && c.Predicate == p.Key:
			if e, ok := g.Entities[c.ObjectID]; ok {
				out = append(out, e)
			}
		case c.ObjectID == from && (c.Predicate == p.Inverse || (p.Symmetric && c.Predicate == p.Key)):
			if e, ok := g.Entities[c.SubjectID]; ok {
				out = append(out, e)
			}
		}
	}
	return out
}

// Render skriver påstandene som kompakte linjer modellen kan lese. Dette er
// hele kontekst-formatet: kort av natur, og hver linje bærer kilden sin.
func (g Graph) Render(paths []Path, maxLines int) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Fra bedriftens kunnskap:\n")
	seen := map[string]bool{}
	n := 0
	for _, p := range paths {
		c := last(p.Claims)
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		b.WriteString("- " + g.Line(c) + "\n")
		n++
		if n >= maxLines {
			break
		}
	}
	return b.String()
}

// Line skriver én påstand som lesbar norsk.
func (g Graph) Line(c Claim) string {
	subj := g.name(c.SubjectID)
	obj := c.ObjectText
	if c.ObjectID != "" {
		obj = g.name(c.ObjectID)
	}
	line := fmt.Sprintf("%s %s %s", subj, c.Predicate, obj)
	if len(c.Qualifiers) > 0 {
		keys := make([]string, 0, len(c.Qualifiers))
		for k := range c.Qualifiers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+c.Qualifiers[k])
		}
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	return line
}

func (g Graph) name(id string) string {
	if e, ok := g.Entities[id]; ok {
		return e.Name
	}
	return id
}
