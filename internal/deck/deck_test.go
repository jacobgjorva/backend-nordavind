package deck

import (
	"encoding/json"
	"strings"
	"testing"
)

// block er kit-DSL-en slik testen leser den (frontend tolker den samme).
type block struct {
	Kind     string  `json:"kind"`
	Field    string  `json:"field"`
	Class    string  `json:"class"`
	Children []block `json:"children"`
}

// Et kit skal være internt konsistent: unike layout-nøkler, felter med hint,
// og ingen blokk som peker på et felt modellen ikke får vite om. Feil her
// gir slides som aldri kan fylles — det skal aldri nå en bruker.
func TestKitsAreConsistent(t *testing.T) {
	for name, k := range All() {
		if len(k.Tokens) == 0 || len(k.Palette) == 0 || len(k.Images) == 0 {
			t.Errorf("%s: mangler tokens, palette eller bilder", name)
		}
		seen := map[string]bool{}
		for _, l := range k.Layouts {
			if seen[l.Key] {
				t.Errorf("%s: layout %q finnes flere ganger", name, l.Key)
			}
			seen[l.Key] = true
			if l.Label == "" || l.Use == "" {
				t.Errorf("%s/%s: mangler label eller use", name, l.Key)
			}
			fields := map[string]bool{}
			for _, f := range l.Fields {
				if f.Kind == "" || f.Hint == "" {
					t.Errorf("%s/%s: feltet %q mangler kind eller hint", name, l.Key, f.Key)
				}
				fields[f.Key] = true
			}
			var blocks []block
			if err := json.Unmarshal(l.Blocks, &blocks); err != nil {
				t.Fatalf("%s/%s: ugyldige blocks: %v", name, l.Key, err)
			}
			if len(blocks) == 0 {
				t.Errorf("%s/%s: ingen blocks", name, l.Key)
			}
			checkFields(t, name, l.Key, blocks, fields)
		}
	}
}

func checkFields(t *testing.T, kit, layout string, blocks []block, fields map[string]bool) {
	t.Helper()
	for _, b := range blocks {
		if b.Field != "" && !fields[b.Field] {
			t.Errorf("%s/%s: blokk %q bruker feltet %q som ikke er definert",
				kit, layout, b.Kind, b.Field)
		}
		checkFields(t, kit, layout, b.Children, fields)
	}
}

// Katalogen er det modellen faktisk ser: alle layouts og felter skal med.
func TestCatalogListsEveryLayout(t *testing.T) {
	k := Get(Default)
	cat := k.Catalog()
	for _, l := range k.Layouts {
		if !strings.Contains(cat, l.Key) {
			t.Errorf("katalogen mangler layout %q", l.Key)
		}
		for _, f := range l.Fields {
			if !strings.Contains(cat, f.Hint) {
				t.Errorf("katalogen mangler hint for %s/%s", l.Key, f.Key)
			}
		}
	}
}

// Ukjent tema skal falle til standardtemaet, aldri gi tomt kit.
func TestGetFallsBackToDefault(t *testing.T) {
	if Get("finnes-ikke").Name != Default {
		t.Error("ukjent tema falt ikke til standardtemaet")
	}
}
