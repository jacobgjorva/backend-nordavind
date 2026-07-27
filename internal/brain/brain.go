// Package brain er den interne hjernen: entiteter, påstander og prosedyrer.
//
// Forskjellen fra kunnskapslappene er hva som kan gjøres med innholdet. En
// lapp kan gjenfinnes; en påstand kan traverseres. «Hvilken dag har jeg møte
// med sjefen min?» krever to fakta som aldri sto i samme setning — det er
// bare mulig når fakta har form (subjekt, predikat, objekt) i stedet for å
// være prosa.
//
// Designregler:
//   - Predikatene er et LUKKET vokabular (predicates.json). Fri tekst her
//     ville gjort grafen usøkbar igjen.
//   - Koden eier identitet, tid og sporbarhet. Modellen fyller innhold.
//   - Alt som lagres peker tilbake på kilden sin. En påstand uten opphav er
//     en påstand vi ikke kan forsvare.
package brain

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed predicates.json
var vocabJSON []byte

// Predicate er én tillatt relasjon.
type Predicate struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	// Object sier om objektet er en annen entitet eller en ren verdi
	// («48 timer»). Styrer validering og hvordan påstanden skrives ut.
	Object string `json:"object"` // entity | text
	Hint   string `json:"hint,omitempty"`
	// Inverse/Symmetric lar traverseringen gå begge veier uten at samme
	// faktum må lagres to ganger.
	Inverse   string `json:"inverse,omitempty"`
	Symmetric bool   `json:"symmetric,omitempty"`
}

type vocabulary struct {
	Version    int         `json:"version"`
	Predicates []Predicate `json:"predicates"`
	Kinds      []string    `json:"kinds"`
}

var vocab = loadVocab()

func loadVocab() vocabulary {
	var v vocabulary
	if err := json.Unmarshal(vocabJSON, &v); err != nil {
		panic("brain: ugyldig predicates.json: " + err.Error())
	}
	if len(v.Predicates) == 0 || len(v.Kinds) == 0 {
		panic("brain: tomt vokabular")
	}
	seen := map[string]bool{}
	for _, p := range v.Predicates {
		if seen[p.Key] {
			panic("brain: predikatet " + p.Key + " finnes flere ganger")
		}
		if p.Object != "entity" && p.Object != "text" {
			panic("brain: predikatet " + p.Key + " har ugyldig object: " + p.Object)
		}
		seen[p.Key] = true
	}
	for _, p := range v.Predicates {
		if p.Inverse != "" && !seen[p.Inverse] {
			panic("brain: ukjent invers " + p.Inverse + " på " + p.Key)
		}
	}
	return v
}

// Predicates gir hele vokabularet i stabil rekkefølge.
func Predicates() []Predicate {
	out := append([]Predicate{}, vocab.Predicates...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Kinds er de tillatte entitetstypene.
func Kinds() []string { return append([]string{}, vocab.Kinds...) }

// Lookup slår opp et predikat.
func Lookup(key string) (Predicate, bool) {
	for _, p := range vocab.Predicates {
		if p.Key == strings.TrimSpace(strings.ToLower(key)) {
			return p, true
		}
	}
	return Predicate{}, false
}

// ValidKind sier om en entitetstype er tillatt.
func ValidKind(kind string) bool {
	for _, k := range vocab.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Entity er en ting hjernen kjenner: en person, kunde, prosedyre, et system.
// Aliaser er det som gjør «sjefen», «Kari H.» og «KH» til samme node.
type Entity struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Aliases []string  `json:"aliases,omitempty"`
	Created time.Time `json:"created_at"`
}

// Claim er ett faktum med form. ObjectID peker på en entitet; ObjectText
// brukes når objektet er en verdi. Nøyaktig én av dem er satt.
type Claim struct {
	ID         string            `json:"id"`
	SubjectID  string            `json:"subject_id"`
	Predicate  string            `json:"predicate"`
	ObjectID   string            `json:"object_id,omitempty"`
	ObjectText string            `json:"object_text,omitempty"`
	Qualifiers map[string]string `json:"qualifiers,omitempty"` // dag, frekvens, sted …
	Confidence float64           `json:"confidence"`
	ValidFrom  time.Time         `json:"valid_from"`
	ValidTo    *time.Time        `json:"valid_to,omitempty"` // nil = gjelder nå
	SourceKind string            `json:"source_kind"`        // chat | document | manual
	SourceID   string            `json:"source_id"`
	StatedBy   string            `json:"stated_by"`
}

// Step er ett steg i en prosedyre. Rekkefølgen er hele poenget — den er
// grunnen til at prosedyrer ikke presses inn i påstandsformen.
type Step struct {
	N        int    `json:"n"`
	Do       string `json:"do"`
	OwnerID  string `json:"owner_id,omitempty"`
	Deadline string `json:"deadline,omitempty"`
}

// Procedure er en fremgangsmåte med steg i rekkefølge.
type Procedure struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Trigger    string     `json:"trigger,omitempty"` // når den gjelder
	Steps      []Step     `json:"steps"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidTo    *time.Time `json:"valid_to,omitempty"`
	SourceKind string     `json:"source_kind"`
	SourceID   string     `json:"source_id"`
}

// Validate sjekker en påstand mot vokabularet FØR den lagres. En gal påstand
// er verre enn ingen: den sprer seg gjennom traverseringen.
func (c Claim) Validate() error {
	p, ok := Lookup(c.Predicate)
	if !ok {
		return fmt.Errorf("ukjent predikat %q", c.Predicate)
	}
	if c.SubjectID == "" {
		return fmt.Errorf("påstand uten subjekt")
	}
	switch p.Object {
	case "entity":
		if c.ObjectID == "" {
			return fmt.Errorf("%q krever en entitet som objekt", p.Key)
		}
		if c.ObjectText != "" {
			return fmt.Errorf("%q tar entitet, ikke fritekst", p.Key)
		}
	case "text":
		if strings.TrimSpace(c.ObjectText) == "" {
			return fmt.Errorf("%q krever en verdi", p.Key)
		}
		if c.ObjectID != "" {
			return fmt.Errorf("%q tar en verdi, ikke en entitet", p.Key)
		}
	}
	if c.SubjectID == c.ObjectID {
		return fmt.Errorf("påstand peker på seg selv")
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		return fmt.Errorf("sikkerhet utenfor 0-1: %v", c.Confidence)
	}
	if c.SourceKind == "" || c.SourceID == "" {
		return fmt.Errorf("påstand uten kilde")
	}
	return nil
}

// Active sier om påstanden gjelder på et gitt tidspunkt.
func (c Claim) Active(at time.Time) bool {
	if c.ValidFrom.After(at) {
		return false
	}
	return c.ValidTo == nil || c.ValidTo.After(at)
}

// Catalog er vokabularet slik uttrekksmodellen ser det: kompakt, med hint
// bare der predikatet ikke er selvforklarende.
func Catalog() string {
	var b strings.Builder
	b.WriteString("Entitetstyper: " + strings.Join(vocab.Kinds, ", ") + "\n")
	b.WriteString("Predikater (bruk KUN disse):\n")
	group := ""
	for _, p := range vocab.Predicates {
		if p.Group != group {
			group = p.Group
			fmt.Fprintf(&b, "  %s:\n", group)
		}
		obj := "→ entitet"
		if p.Object == "text" {
			obj = "→ verdi"
		}
		fmt.Fprintf(&b, "    %s %s", p.Key, obj)
		if p.Hint != "" {
			fmt.Fprintf(&b, " (%s)", p.Hint)
		}
		b.WriteString("\n")
	}
	return b.String()
}
