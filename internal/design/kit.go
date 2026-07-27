// Package design er motoren bak alt brukeren lager visuelt: presentasjoner,
// flyers, kampanjer. Ett dokument er en liste FLATER, hver flate en ferdig
// designet layout fra et KITT der modellen kun fyller navngitte felt.
//
// Grunnregelen: kittet eier komposisjon, typografi og farger; koden eier
// id-er, rekkefølge, validering og lagring; modellen eier ordene. Et nytt
// uttrykk er en JSON-fil, aldri en ny kodesti.
package design

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed kits/*.json
var kitFS embed.FS

// Slot er ett felt modellen kan fylle på en flate.
type Slot struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"` // text | markdown | list | image | images | widget | widgets
	Hint     string `json:"hint"`
	Required bool   `json:"required,omitempty"`
}

// Layout er én ferdig komposisjon. Blocks tolkes kun av frontend-rendereren.
type Layout struct {
	Key   string `json:"key"`
	Use   string `json:"use"`
	Slots []Slot `json:"slots"`
	// Fallback er layouten en flate faller til når et annet kitt mangler
	// denne nøkkelen (restyle). Tom = fall til kittets første layout.
	Fallback string          `json:"fallback,omitempty"`
	Blocks   json.RawMessage `json:"blocks"`
}

// Format er flatens fysiske form. Modellen ser den, men bestemmer den aldri.
type Format struct {
	Ratio string `json:"ratio"`
	Width int    `json:"width"`
}

// Kit er ett uttrykk for én dokumenttype.
type Kit struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"` // deck | flyer | campaign …
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Format      Format            `json:"format"`
	Tokens      map[string]string `json:"tokens"`
	Palette     []string          `json:"palette"`
	Assets      []string          `json:"assets"`
	Layouts     []Layout          `json:"layouts"`
}

// Default er kittet som brukes når ingenting er valgt.
const Default = "noir"

var kits = load()

// load leser kittene ved oppstart. En ødelagt kitt-fil er en programmeringsfeil
// og skal stoppe oppstarten — aldri gi rare dokumenter i produksjon.
func load() map[string]Kit {
	out := map[string]Kit{}
	entries, err := kitFS.ReadDir("kits")
	if err != nil {
		panic("design: kunne ikke lese kits: " + err.Error())
	}
	for _, e := range entries {
		b, err := kitFS.ReadFile("kits/" + e.Name())
		if err != nil {
			panic("design: kunne ikke lese " + e.Name() + ": " + err.Error())
		}
		var k Kit
		if err := json.Unmarshal(b, &k); err != nil {
			panic("design: ugyldig kitt " + e.Name() + ": " + err.Error())
		}
		if k.Name == "" || k.Type == "" || len(k.Layouts) == 0 {
			panic("design: kitt " + e.Name() + " mangler name, type eller layouts")
		}
		out[k.Name] = k
	}
	return out
}

// All gir alle kitt (galleriet i frontend).
func All() map[string]Kit { return kits }

// Get slår opp et kitt; ukjent navn faller til standardkittet.
func Get(name string) Kit {
	if k, ok := kits[name]; ok {
		return k
	}
	return kits[Default]
}

// Names gir kitt-navnene i stabil rekkefølge.
func Names() []string {
	out := make([]string, 0, len(kits))
	for n := range kits {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// OfType gir kittene for én dokumenttype (galleriets faner).
func OfType(t string) []Kit {
	var out []Kit
	for _, n := range Names() {
		if kits[n].Type == t {
			out = append(out, kits[n])
		}
	}
	return out
}

// Layout slår opp én layout i kittet.
func (k Kit) Layout(key string) (Layout, bool) {
	for _, l := range k.Layouts {
		if l.Key == key {
			return l, true
		}
	}
	return Layout{}, false
}

// LayoutKeys gir layout-nøklene i rekkefølge.
func (k Kit) LayoutKeys() []string {
	out := make([]string, 0, len(k.Layouts))
	for _, l := range k.Layouts {
		out = append(out, l.Key)
	}
	return out
}

// Slot slår opp ett felt på en layout.
func (l Layout) Slot(key string) (Slot, bool) {
	for _, s := range l.Slots {
		if s.Key == key {
			return s, true
		}
	}
	return Slot{}, false
}

// AssetNames gir bildene uten filendelse. Modellen skal aldri kjenne til om
// et motiv er .png eller .jpg — bytter man ut filen, står dokumentet.
func (k Kit) AssetNames() []string {
	out := make([]string, 0, len(k.Assets))
	for _, f := range k.Assets {
		if i := strings.LastIndex(f, "."); i > 0 {
			f = f[:i]
		}
		out = append(out, f)
	}
	return out
}

// Catalog er kittet slik modellen ser det: én linje per layout, felt med
// «!» for påkrevd. Kompakt med vilje — dette betales for i hver eneste tur.
//
//	title: åpningsflate | title!(3-7 ord) sub(én linje) image!(bg-N)
func (k Kit) Catalog() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Kitt «%s» (%s, %s): %s\nBilder: %s\nFlate-typer:\n",
		k.Name, k.Type, k.Format.Ratio, k.Description, strings.Join(k.AssetNames(), " "))
	for _, l := range k.Layouts {
		fmt.Fprintf(&b, "%s: %s |", l.Key, l.Use)
		for _, s := range l.Slots {
			req := ""
			if s.Required {
				req = "!"
			}
			fmt.Fprintf(&b, " %s%s(%s)", s.Key, req, shortHint(s.Hint))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// LayoutCatalog er katalogen for ÉN layout — det redigering trenger, i stedet
// for hele kittet.
func (k Kit) LayoutCatalog(key string) string {
	l, ok := k.Layout(key)
	if !ok {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s |", l.Key, l.Use)
	for _, s := range l.Slots {
		req := ""
		if s.Required {
			req = "!"
		}
		fmt.Fprintf(&b, " %s%s(%s)", s.Key, req, shortHint(s.Hint))
	}
	return b.String()
}

// shortHint kutter hintet ved første komma eller punktum: modellen trenger
// pekepinnen, ikke hele setningen.
func shortHint(h string) string {
	if i := strings.IndexAny(h, ",."); i > 0 {
		h = h[:i]
	}
	return strings.TrimSpace(h)
}

// NeedsData sier om dokumentet har flater som henter tall fra databasen.
// Uten slike flater skal SQL-skjemaet aldri sendes til modellen — det er
// bare tokens.
func (k Kit) NeedsData(doc Doc) bool {
	for _, s := range doc.Surfaces {
		l, ok := k.Layout(s.Layout)
		if !ok {
			continue
		}
		for _, sl := range l.Slots {
			if sl.Kind == "widget" || sl.Kind == "widgets" {
				return true
			}
		}
	}
	return false
}

// HasDataLayouts sier om kittet i det hele tatt kan vise data.
func (k Kit) HasDataLayouts() bool {
	for _, l := range k.Layouts {
		for _, s := range l.Slots {
			if s.Kind == "widget" || s.Kind == "widgets" {
				return true
			}
		}
	}
	return false
}
