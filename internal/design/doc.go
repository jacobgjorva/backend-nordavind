package design

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// Surface er én flate: en slide, en flyer-side, en plakat. Fields inneholder
// kun felt kittets layout definerer — resten avvises ved validering.
type Surface struct {
	ID     string         `json:"id"`
	Layout string         `json:"layout"`
	Fields map[string]any `json:"fields"`
}

// Doc er hele dokumentet slik det lagres.
type Doc struct {
	Type     string            `json:"type"` // alltid "design" i widget-specen
	Kind     string            `json:"kind"` // deck | flyer | campaign …
	Kit      string            `json:"kit"`
	Title    string            `json:"title,omitempty"`
	Style    map[string]string `json:"style,omitempty"` // token-overrides oppå kittet
	Surfaces []Surface         `json:"surfaces"`
}

// SurfaceInput er én flate slik modellen leverer den i compose: layout pluss
// feltene, uten id — id-er er kodens ansvar.
type SurfaceInput struct {
	Layout string         `json:"layout"`
	Fields map[string]any `json:"fields"`
}

// Report er hva modellen får vite etter en operasjon: hva som ble gjort, og
// nøyaktig hva som ble avvist. Presise avvisninger er billigere enn en ny
// runde med gjetting.
type Report struct {
	OK       bool
	Applied  int
	Problems []string
}

func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d flater lagret", r.Applied)
	if len(r.Problems) > 0 {
		b.WriteString(". Avvist: " + strings.Join(r.Problems, "; "))
	}
	return b.String()
}

// newID gir en kort, stabil flate-id. Kollisjon er praktisk talt utelukket,
// og modellen får aldri finne på id-er selv.
func newID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return "f" + string(b)
}

// Compose bygger HELE dokumentet i én operasjon. Dette er hovedveien inn ved
// nybygg: én modelltur, ingen kjede av verktøykall som resender historikken.
func Compose(kit Kit, in []SurfaceInput) (Doc, Report) {
	doc := Doc{Type: "design", Kind: kit.Type, Kit: kit.Name}
	rep := Report{OK: true}
	for i, s := range in {
		sf, probs := build(kit, s.Layout, s.Fields, i+1)
		rep.Problems = append(rep.Problems, probs...)
		if sf == nil {
			continue
		}
		doc.Surfaces = append(doc.Surfaces, *sf)
	}
	rep.Applied = len(doc.Surfaces)
	rep.OK = rep.Applied > 0
	return doc, rep
}

// build lager én validert flate. Ukjent layout forkastes; ukjente felt
// droppes; manglende påkrevde felt rapporteres men stopper ikke flaten —
// et halvfylt utkast er bedre enn ingenting, og brukeren ser hullet.
func build(kit Kit, layout string, fields map[string]any, pos int) (*Surface, []string) {
	var probs []string
	l, ok := kit.Layout(layout)
	if !ok {
		return nil, []string{fmt.Sprintf("flate %d: ukjent layout %q (gyldige: %s)",
			pos, layout, strings.Join(kit.LayoutKeys(), ", "))}
	}
	clean := map[string]any{}
	for k, v := range fields {
		if _, ok := l.Slot(k); !ok {
			probs = append(probs, fmt.Sprintf("flate %d (%s): feltet %q finnes ikke", pos, layout, k))
			continue
		}
		if isEmpty(v) {
			continue
		}
		clean[k] = v
	}
	for _, s := range l.Slots {
		if s.Required && isEmpty(clean[s.Key]) {
			probs = append(probs, fmt.Sprintf("flate %d (%s): mangler %s", pos, layout, s.Key))
		}
	}
	return &Surface{ID: newID(), Layout: layout, Fields: clean}, probs
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// Op er én redigering av et eksisterende dokument.
type Op struct {
	Action string         `json:"action"` // set | add | remove | move
	ID     string         `json:"id"`
	After  string         `json:"after"`
	Layout string         `json:"layout"`
	Fields map[string]any `json:"fields"`
}

// Apply utfører én redigering. Feltene som ikke nevnes står urørt — det er
// hele grunnen til at brukerens egne rettinger i canvaset overlever at
// modellen endrer noe annet på samme flate.
func Apply(kit Kit, doc *Doc, op Op) Report {
	switch op.Action {
	case "add":
		sf, probs := build(kit, op.Layout, op.Fields, len(doc.Surfaces)+1)
		if sf == nil {
			return Report{Problems: probs}
		}
		doc.Surfaces = insert(doc.Surfaces, *sf, op.After)
		return Report{OK: true, Applied: 1, Problems: probs}

	case "remove":
		i := indexOf(doc.Surfaces, op.ID)
		if i < 0 {
			return Report{Problems: []string{"ukjent flate " + op.ID}}
		}
		doc.Surfaces = append(doc.Surfaces[:i], doc.Surfaces[i+1:]...)
		return Report{OK: true, Applied: 1}

	case "move":
		i := indexOf(doc.Surfaces, op.ID)
		if i < 0 {
			return Report{Problems: []string{"ukjent flate " + op.ID}}
		}
		sf := doc.Surfaces[i]
		doc.Surfaces = append(doc.Surfaces[:i], doc.Surfaces[i+1:]...)
		doc.Surfaces = insert(doc.Surfaces, sf, op.After)
		return Report{OK: true, Applied: 1}

	default: // set
		i := indexOf(doc.Surfaces, op.ID)
		if i < 0 {
			return Report{Problems: []string{"ukjent flate " + op.ID + " (bruk add for en ny)"}}
		}
		return patch(kit, &doc.Surfaces[i], op)
	}
}

// patch endrer felt på én flate, og bytter layout hvis det er bedt om.
func patch(kit Kit, sf *Surface, op Op) Report {
	layout := sf.Layout
	if op.Layout != "" {
		if _, ok := kit.Layout(op.Layout); !ok {
			return Report{Problems: []string{fmt.Sprintf("ukjent layout %q", op.Layout)}}
		}
		layout = op.Layout
	}
	l, _ := kit.Layout(layout)
	var probs []string
	next := map[string]any{}
	// Layout-bytte: behold bare felt den nye layouten faktisk har.
	for k, v := range sf.Fields {
		if _, ok := l.Slot(k); ok {
			next[k] = v
		}
	}
	for k, v := range op.Fields {
		if _, ok := l.Slot(k); !ok {
			probs = append(probs, fmt.Sprintf("%s: feltet %q finnes ikke", layout, k))
			continue
		}
		// Tom streng er måten et felt fjernes på (f.eks. en bildetekst).
		if s, isStr := v.(string); isStr && s == "" {
			delete(next, k)
			continue
		}
		next[k] = v
	}
	sf.Layout = layout
	sf.Fields = next
	return Report{OK: true, Applied: 1, Problems: probs}
}

func insert(list []Surface, sf Surface, after string) []Surface {
	if after != "" {
		if i := indexOf(list, after); i >= 0 {
			out := append([]Surface{}, list[:i+1]...)
			out = append(out, sf)
			return append(out, list[i+1:]...)
		}
	}
	return append(list, sf)
}

func indexOf(list []Surface, id string) int {
	if id == "" {
		return -1
	}
	for i, s := range list {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// Restyle bytter kitt og beholder innholdet. Layouts som ikke finnes i det
// nye kittet følger fallback-kjeden; felt den nye layouten ikke har, faller
// bort. Kittene deler et felles vokabular av layout-nøkler nettopp for at
// dette skal gjøre minst mulig skade.
func Restyle(from, to Kit, doc *Doc) Report {
	rep := Report{OK: true}
	for i := range doc.Surfaces {
		sf := &doc.Surfaces[i]
		key := resolveLayout(from, to, sf.Layout)
		if key != sf.Layout {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("%s finnes ikke i %s — brukte %s", sf.Layout, to.Name, key))
		}
		l, _ := to.Layout(key)
		next := map[string]any{}
		for k, v := range sf.Fields {
			if _, ok := l.Slot(k); ok {
				next[k] = v
			}
		}
		sf.Layout, sf.Fields = key, next
		rep.Applied++
	}
	doc.Kit, doc.Kind = to.Name, to.Type
	return rep
}

// resolveLayout finner nærmeste slektning i målkittet.
func resolveLayout(from, to Kit, key string) string {
	if _, ok := to.Layout(key); ok {
		return key
	}
	// Følg kildekittets fallback-kjede så langt den rekker.
	seen := map[string]bool{}
	cur := key
	for range from.Layouts {
		l, ok := from.Layout(cur)
		if !ok || l.Fallback == "" || seen[l.Fallback] {
			break
		}
		seen[l.Fallback] = true
		cur = l.Fallback
		if _, ok := to.Layout(cur); ok {
			return cur
		}
	}
	return to.Layouts[0].Key
}

// Parse leser et lagret dokument. Gamle presentasjoner (type "deck" med
// «slides» og felt på toppnivå) leses uendret — ingen migrering av data.
func Parse(raw []byte) (Doc, bool) {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return Doc{}, false
	}
	if _, ok := probe["surfaces"]; ok {
		var d Doc
		if err := json.Unmarshal(raw, &d); err != nil {
			return Doc{}, false
		}
		if d.Kit == "" {
			d.Kit = Default
		}
		return d, true
	}
	if t, _ := probe["type"].(string); t != "deck" {
		return Doc{}, false
	}
	return fromDeck(probe), true
}

// fromDeck oversetter den gamle deck-specen til flater i minnet.
var deckFields = []string{"title", "content", "image", "images", "widget", "widgets"}

func fromDeck(spec map[string]any) Doc {
	d := Doc{Type: "design", Kind: "deck", Kit: Default}
	if t, ok := spec["title"].(string); ok {
		d.Title = t
	}
	if th, ok := spec["theme"].(string); ok && th != "" {
		d.Kit = th
	}
	if st, ok := spec["style"].(map[string]any); ok {
		d.Style = map[string]string{}
		for k, v := range st {
			if s, ok := v.(string); ok {
				d.Style[k] = s
			}
		}
	}
	slides, _ := spec["slides"].([]any)
	for _, raw := range slides {
		sl, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		sf := Surface{Fields: map[string]any{}}
		sf.ID, _ = sl["id"].(string)
		if sf.ID == "" {
			sf.ID = newID()
		}
		sf.Layout, _ = sl["layout"].(string)
		for _, f := range deckFields {
			if v, ok := sl[f]; ok && !isEmpty(v) {
				sf.Fields[f] = v
			}
		}
		d.Surfaces = append(d.Surfaces, sf)
	}
	return d
}

// NeedsSQL gir flatene som har en visualisering uten spørring — de eneste
// som trenger databaseskjemaet. Er listen tom, skal skjemaet aldri sendes.
func (d Doc) NeedsSQL() []Surface {
	var out []Surface
	for _, s := range d.Surfaces {
		for _, f := range []string{"widget", "widgets"} {
			switch v := s.Fields[f].(type) {
			case map[string]any:
				if missingSQL(v) {
					out = append(out, s)
				}
			case []any:
				for _, w := range v {
					if m, ok := w.(map[string]any); ok && missingSQL(m) {
						out = append(out, s)
						break
					}
				}
			}
		}
	}
	return out
}

func missingSQL(w map[string]any) bool {
	sql, _ := w["sql"].(string)
	conn, _ := w["connection_id"].(string)
	return strings.TrimSpace(sql) == "" || strings.TrimSpace(conn) == ""
}

// State er dokumentet slik modellen ser det ved redigering: én linje per
// flate med id, layout og en kort forhåndsvisning. Kort med vilje.
func (d Doc) State() string {
	if len(d.Surfaces) == 0 {
		return "Dokumentet er tomt."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Flatene nå (%d):\n", len(d.Surfaces))
	for i, s := range d.Surfaces {
		fmt.Fprintf(&b, "%d. %s %s", i+1, s.ID, s.Layout)
		for _, f := range []string{"title", "content"} {
			if v, _ := s.Fields[f].(string); v != "" {
				fmt.Fprintf(&b, " %s=%q", f, preview(v, 50))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func preview(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
