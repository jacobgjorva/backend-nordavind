// Package deck eier presentasjons-kittene: ferdig designede temaer med et
// fast sett slides. Ett kit = én JSON-fil i kits/ — den er kilden både for
// hva modellen får se (katalogen under) og for hvordan frontend rendrer
// sliden (blocks sendes rått ut på /v1/deck/kits).
//
// Designregel: nye temaer og nye slide-typer er nye JSON-filer/-blokker,
// aldri ny kode. Modellen velger layout og fyller felter — den finner aldri
// på komposisjon eller styling.
package deck

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed kits/*.json
var kitFS embed.FS

// Field er ett felt modellen kan fylle på en slide.
type Field struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"` // text | markdown | list | image | images | widget | widgets
	Hint     string `json:"hint"`
	Required bool   `json:"required,omitempty"`
}

// Layout er én ferdig slide-komposisjon. Blocks tolkes kun av frontend.
type Layout struct {
	Key    string          `json:"key"`
	Label  string          `json:"label"`
	Use    string          `json:"use"`
	Fields []Field         `json:"fields"`
	Blocks json.RawMessage `json:"blocks"`
}

// Kit er ett tema med tokens, bilder og layouts.
type Kit struct {
	Name        string            `json:"name"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Tokens      map[string]string `json:"tokens"`
	Palette     []string          `json:"palette"`
	Images      []string          `json:"images"`
	Layouts     []Layout          `json:"layouts"`
}

var kits = loadKits()

// loadKits leser alle kits ved oppstart. En ødelagt kit-fil er en
// programmeringsfeil og skal stoppe oppstarten, ikke gi rare presentasjoner.
func loadKits() map[string]Kit {
	out := map[string]Kit{}
	entries, err := kitFS.ReadDir("kits")
	if err != nil {
		panic("deck: kunne ikke lese kits: " + err.Error())
	}
	for _, e := range entries {
		b, err := kitFS.ReadFile("kits/" + e.Name())
		if err != nil {
			panic("deck: kunne ikke lese " + e.Name() + ": " + err.Error())
		}
		var k Kit
		if err := json.Unmarshal(b, &k); err != nil {
			panic("deck: ugyldig kit " + e.Name() + ": " + err.Error())
		}
		if k.Name == "" || len(k.Layouts) == 0 {
			panic("deck: kit " + e.Name() + " mangler name eller layouts")
		}
		out[k.Name] = k
	}
	return out
}

// Default er temaet som brukes når ingenting er valgt.
const Default = "noir"

// All gir alle kits (til /v1/deck/kits).
func All() map[string]Kit { return kits }

// Get slår opp et kit; ukjent navn faller til standardtemaet.
func Get(name string) Kit {
	if k, ok := kits[name]; ok {
		return k
	}
	return kits[Default]
}

// Names gir tema-navnene i stabil rekkefølge.
func Names() []string {
	out := make([]string, 0, len(kits))
	for n := range kits {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// LayoutKeys gir layout-nøklene i temaet (til verktøy-schemaet).
func (k Kit) LayoutKeys() []string {
	out := make([]string, 0, len(k.Layouts))
	for _, l := range k.Layouts {
		out = append(out, l.Key)
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

// ImageNames gir bildene uten filendelse. Modellen skal aldri kjenne til om
// et motiv er .png eller .jpg — bytter man ut filen skal decket stå.
func (k Kit) ImageNames() []string {
	out := make([]string, 0, len(k.Images))
	for _, f := range k.Images {
		if i := strings.LastIndex(f, "."); i > 0 {
			f = f[:i]
		}
		out = append(out, f)
	}
	return out
}

// Catalog er kit-katalogen slik modellen ser den: én linje per layout med
// feltene den kan fylle. Genereres fra JSON-en, så nye slides dukker opp i
// prompten uten kodeendring.
func (k Kit) Catalog() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tema «%s»: %s\nBilder i temaet: %s\nSlide-typene du kan velge:\n",
		k.Name, k.Description, strings.Join(k.ImageNames(), ", "))
	for _, l := range k.Layouts {
		fmt.Fprintf(&b, "- %s (%s). %s\n", l.Key, l.Label, l.Use)
		for _, f := range l.Fields {
			req := ""
			if f.Required {
				req = ", påkrevd"
			}
			fmt.Fprintf(&b, "    %s (%s%s): %s\n", f.Key, f.Kind, req, f.Hint)
		}
	}
	return b.String()
}
