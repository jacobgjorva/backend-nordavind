package design

import (
	"encoding/json"
	"strings"
	"testing"
)

func kit(t *testing.T) Kit {
	t.Helper()
	return Get(Default)
}

// Et kitt skal være internt konsistent: unike layouts, felt med hint, og
// ingen blokk som peker på et felt modellen ikke får vite om.
func TestKitsAreConsistent(t *testing.T) {
	type block struct {
		Kind     string  `json:"kind"`
		Field    string  `json:"field"`
		Children []block `json:"children"`
	}
	var check func(t *testing.T, kit, layout string, bs []block, slots map[string]bool)
	check = func(t *testing.T, kitName, layout string, bs []block, slots map[string]bool) {
		t.Helper()
		for _, b := range bs {
			if b.Field != "" && !slots[b.Field] {
				t.Errorf("%s/%s: blokk %q bruker udefinert felt %q", kitName, layout, b.Kind, b.Field)
			}
			check(t, kitName, layout, b.Children, slots)
		}
	}
	for name, k := range All() {
		if k.Type == "" || k.Format.Ratio == "" || len(k.Tokens) == 0 {
			t.Errorf("%s: mangler type, format eller tokens", name)
		}
		seen := map[string]bool{}
		for _, l := range k.Layouts {
			if seen[l.Key] {
				t.Errorf("%s: layout %q finnes flere ganger", name, l.Key)
			}
			seen[l.Key] = true
			if l.Use == "" || len(l.Slots) == 0 {
				t.Errorf("%s/%s: mangler use eller slots", name, l.Key)
			}
			slots := map[string]bool{}
			for _, s := range l.Slots {
				if s.Kind == "" || s.Hint == "" {
					t.Errorf("%s/%s: feltet %q mangler kind eller hint", name, l.Key, s.Key)
				}
				slots[s.Key] = true
			}
			var bs []block
			if err := json.Unmarshal(l.Blocks, &bs); err != nil {
				t.Fatalf("%s/%s: ugyldige blocks: %v", name, l.Key, err)
			}
			check(t, name, l.Key, bs, slots)
		}
		for _, l := range k.Layouts {
			if l.Fallback == "" {
				continue
			}
			if _, ok := k.Layout(l.Fallback); !ok {
				t.Errorf("%s/%s: fallback %q finnes ikke", name, l.Key, l.Fallback)
			}
		}
	}
}

// Katalogen er det vi betaler for i hver tur — den skal være liten OG
// komplett. Ryker taket, er det et bevisst valg, ikke en ulykke.
func TestCatalogIsCompactAndComplete(t *testing.T) {
	k := kit(t)
	c := k.Catalog()
	for _, l := range k.Layouts {
		if !strings.Contains(c, l.Key+":") {
			t.Errorf("katalogen mangler layout %q", l.Key)
		}
		for _, s := range l.Slots {
			if !strings.Contains(c, s.Key) {
				t.Errorf("katalogen mangler feltet %s/%s", l.Key, s.Key)
			}
		}
	}
	if approx := len(c) / 4; approx > 450 {
		t.Errorf("katalogen er %d tegn (~%d tokens) — for dyr per tur", len(c), approx)
	}
}

// Compose er hovedveien inn: hele dokumentet i én operasjon.
func TestComposeBuildsWholeDocument(t *testing.T) {
	k := kit(t)
	doc, rep := Compose(k, []SurfaceInput{
		{Layout: "title", Fields: map[string]any{"title": "Året 2026", "image": "bg-1"}},
		{Layout: "bullets", Fields: map[string]any{"title": "Tre funn", "content": "- ett\n- to"}},
	})
	if !rep.OK || rep.Applied != 2 || len(doc.Surfaces) != 2 {
		t.Fatalf("forventet to flater, fikk %d (%v)", rep.Applied, rep.Problems)
	}
	if doc.Kind != "deck" || doc.Kit != "noir" {
		t.Errorf("dokumentet mangler type/kitt: %+v", doc)
	}
	if doc.Surfaces[0].ID == "" || doc.Surfaces[0].ID == doc.Surfaces[1].ID {
		t.Error("flatene skal ha egne id-er fra koden")
	}
}

// Ukjent layout eller felt skal avvises presist, ikke lagres som søppel.
func TestComposeRejectsUnknown(t *testing.T) {
	k := kit(t)
	doc, rep := Compose(k, []SurfaceInput{
		{Layout: "finnes-ikke", Fields: map[string]any{"title": "x"}},
		{Layout: "bullets", Fields: map[string]any{"title": "Ok", "content": "- a", "tull": "x"}},
	})
	if len(doc.Surfaces) != 1 {
		t.Fatalf("ugyldig layout skulle vært forkastet, fikk %d flater", len(doc.Surfaces))
	}
	if _, ok := doc.Surfaces[0].Fields["tull"]; ok {
		t.Error("ukjent felt ble lagret")
	}
	if len(rep.Problems) < 2 {
		t.Errorf("forventet presise avvisninger, fikk %v", rep.Problems)
	}
	if !strings.Contains(rep.Problems[0], "gyldige:") {
		t.Errorf("avvisningen skal fortelle hva som er gyldig: %q", rep.Problems[0])
	}
}

// Manglende påkrevd felt stopper ikke flaten, men rapporteres.
func TestComposeReportsMissingRequired(t *testing.T) {
	doc, rep := Compose(kit(t), []SurfaceInput{
		{Layout: "title", Fields: map[string]any{"title": "Uten bilde"}},
	})
	if len(doc.Surfaces) != 1 {
		t.Fatal("flaten skulle vært beholdt")
	}
	if len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "mangler image") {
		t.Errorf("forventet melding om manglende image, fikk %v", rep.Problems)
	}
}

// Kjernen i redigering: en patch rører KUN feltene den nevner, så brukerens
// egen retting i canvaset overlever at modellen endrer noe annet.
func TestApplySetKeepsUntouchedFields(t *testing.T) {
	k := kit(t)
	doc, _ := Compose(k, []SurfaceInput{
		{Layout: "bullets", Fields: map[string]any{"title": "Brukerens tittel", "content": "- ett"}},
	})
	id := doc.Surfaces[0].ID
	rep := Apply(k, &doc, Op{Action: "set", ID: id, Fields: map[string]any{"content": "- to"}})
	if !rep.OK {
		t.Fatalf("patch feilet: %v", rep.Problems)
	}
	f := doc.Surfaces[0].Fields
	if f["title"] != "Brukerens tittel" {
		t.Errorf("tittelen ble overskrevet: %v", f["title"])
	}
	if f["content"] != "- to" {
		t.Errorf("innholdet ble ikke oppdatert: %v", f["content"])
	}
}

// Tom streng fjerner et felt (f.eks. en bildetekst).
func TestApplySetRemovesOnEmptyString(t *testing.T) {
	k := kit(t)
	doc, _ := Compose(k, []SurfaceInput{
		{Layout: "bullets", Fields: map[string]any{"title": "Noe", "content": "- a"}},
	})
	Apply(k, &doc, Op{Action: "set", ID: doc.Surfaces[0].ID, Fields: map[string]any{"title": ""}})
	if _, ok := doc.Surfaces[0].Fields["title"]; ok {
		t.Error("tittelen skulle vært fjernet")
	}
}

// Layout-bytte beholder felt den nye layouten faktisk har.
func TestApplyChangeLayoutDropsForeignFields(t *testing.T) {
	k := kit(t)
	doc, _ := Compose(k, []SurfaceInput{
		{Layout: "title", Fields: map[string]any{"title": "T", "content": "sub", "image": "bg-1"}},
	})
	Apply(k, &doc, Op{Action: "set", ID: doc.Surfaces[0].ID, Layout: "bullets"})
	f := doc.Surfaces[0].Fields
	if doc.Surfaces[0].Layout != "bullets" {
		t.Fatal("layouten ble ikke byttet")
	}
	if _, ok := f["image"]; ok {
		t.Error("bildet skulle falt bort — bullets har ikke image")
	}
	if f["title"] != "T" {
		t.Error("tittelen skulle overlevd byttet")
	}
}

func TestApplyAddMoveRemove(t *testing.T) {
	k := kit(t)
	doc, _ := Compose(k, []SurfaceInput{
		{Layout: "bullets", Fields: map[string]any{"title": "A", "content": "- a"}},
		{Layout: "bullets", Fields: map[string]any{"title": "B", "content": "- b"}},
	})
	first, second := doc.Surfaces[0].ID, doc.Surfaces[1].ID

	Apply(k, &doc, Op{Action: "add", After: first,
		Layout: "statement", Fields: map[string]any{"content": "Midt i"}})
	if doc.Surfaces[1].Layout != "statement" {
		t.Fatalf("ny flate havnet feil: %v", layouts(doc))
	}
	mid := doc.Surfaces[1].ID

	Apply(k, &doc, Op{Action: "move", ID: mid, After: second})
	if doc.Surfaces[2].ID != mid {
		t.Fatalf("flytting feilet: %v", layouts(doc))
	}
	if rep := Apply(k, &doc, Op{Action: "remove", ID: mid}); !rep.OK || len(doc.Surfaces) != 2 {
		t.Fatalf("sletting feilet: %v", rep.Problems)
	}
	if rep := Apply(k, &doc, Op{Action: "set", ID: "finnes-ikke"}); rep.OK {
		t.Error("ukjent id skulle vært avvist")
	}
}

// Restyle beholder innholdet når kittene deler layout-vokabular.
func TestRestyleKeepsContent(t *testing.T) {
	k := kit(t)
	doc, _ := Compose(k, []SurfaceInput{
		{Layout: "bullets", Fields: map[string]any{"title": "Behold meg", "content": "- a"}},
	})
	rep := Restyle(k, k, &doc)
	if !rep.OK || doc.Surfaces[0].Fields["title"] != "Behold meg" {
		t.Errorf("innholdet forsvant ved restyle: %v", doc.Surfaces)
	}
}

// Gamle presentasjoner skal leses uendret — ingen datamigrering.
func TestParseReadsLegacyDeck(t *testing.T) {
	raw := []byte(`{"type":"deck","title":"Gammel","theme":"noir","slides":[
		{"id":"s1","layout":"title","title":"Hei","image":"bg-1"},
		{"layout":"bullets","content":"- a"}]}`)
	d, ok := Parse(raw)
	if !ok {
		t.Fatal("klarte ikke lese gammelt deck")
	}
	if d.Title != "Gammel" || len(d.Surfaces) != 2 {
		t.Fatalf("feil oversettelse: %+v", d)
	}
	if d.Surfaces[0].ID != "s1" || d.Surfaces[0].Fields["title"] != "Hei" {
		t.Errorf("felt gikk tapt: %+v", d.Surfaces[0])
	}
	if d.Surfaces[1].ID == "" {
		t.Error("flate uten id skulle fått en")
	}
}

func TestParseRoundTripsNewFormat(t *testing.T) {
	doc, _ := Compose(kit(t), []SurfaceInput{
		{Layout: "statement", Fields: map[string]any{"content": "Ett poeng"}},
	})
	raw, _ := json.Marshal(doc)
	back, ok := Parse(raw)
	if !ok || len(back.Surfaces) != 1 || back.Surfaces[0].Fields["content"] != "Ett poeng" {
		t.Fatalf("rundturen mistet innhold: %+v", back)
	}
}

// SQL-skjemaet skal bare følge med når dokumentet faktisk viser tall.
func TestNeedsDataOnlyForDataLayouts(t *testing.T) {
	k := kit(t)
	text, _ := Compose(k, []SurfaceInput{
		{Layout: "statement", Fields: map[string]any{"content": "Ingen tall her"}},
	})
	if k.NeedsData(text) {
		t.Error("ren tekst skal ikke dra med seg databaseskjemaet")
	}
	data, _ := Compose(k, []SurfaceInput{
		{Layout: "chart", Fields: map[string]any{"title": "Salg", "widget": map[string]any{"type": "bar"}}},
	})
	if !k.NeedsData(data) {
		t.Error("graf-flate trenger skjemaet")
	}
}

func layouts(d Doc) []string {
	out := make([]string, len(d.Surfaces))
	for i, s := range d.Surfaces {
		out[i] = s.Layout
	}
	return out
}
