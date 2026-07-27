package brain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Uttrekket: fra tekst til entiteter, påstander og prosedyrer.
//
// Modellen leverer ET forslag i fast JSON-form. Alt som ikke passer
// vokabularet forkastes HER, før noe når lageret — en gal påstand sprer seg
// gjennom traverseringen og er verre enn ingen påstand.

// ExtractSystem er instruksen til uttrekksmodellen. Katalogen legges på av
// Prompt() så vokabular-endringer aldri må speiles i en tekststreng.
const ExtractSystem = "Du trekker ut BEDRIFTS-INTERNE fakta fra en tekst og skriver dem som " +
	"påstander: hvem/hva, relasjon, hvem/hva. " +
	"Ta bare med det som er spesifikt for denne bedriften — rutiner, roller, ansvar, " +
	"faste avtaler, egne terskler og frister. Lakmustest: ville en kompetent AI visst " +
	"dette uten å få det fortalt? Er svaret ja, utelat det. " +
	"Ta ALDRI med allmennkunnskap, det AI-en selv svarte, nettsøk-resultater, " +
	"meninger, engangshendelser eller sensitive personopplysninger. " +
	"Ved tvil: utelat. De fleste tekster gir INGEN påstander.\n" +
	"Er teksten en fremgangsmåte med rekkefølge, skriv den som en prosedyre med steg " +
	"i stedet for løse påstander — rekkefølgen er selve innholdet.\n" +
	"Bruk KUN predikatene under. Passer et faktum ikke inn, dropp det.\n" +
	"Svar KUN med JSON: {\"entities\":[{\"name\":\"…\",\"kind\":\"…\",\"aliases\":[\"…\"]}]," +
	"\"claims\":[{\"subject\":\"navn\",\"predicate\":\"…\",\"object\":\"navn eller verdi\"," +
	"\"qualifiers\":{\"dag\":\"mandag\",\"frekvens\":\"ukentlig\"},\"confidence\":0.8}]," +
	"\"procedures\":[{\"name\":\"…\",\"trigger\":\"når den gjelder\"," +
	"\"steps\":[{\"n\":1,\"do\":\"…\",\"owner\":\"navn\",\"deadline\":\"…\"}]}]}"

// Prompt er hele systemteksten: instruks pluss det gjeldende vokabularet.
func Prompt() string {
	return ExtractSystem + "\n\n" + Catalog()
}

// Raw er formen modellen svarer i.
type Raw struct {
	Entities []struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Aliases []string `json:"aliases"`
	} `json:"entities"`
	Claims []struct {
		Subject    string            `json:"subject"`
		Predicate  string            `json:"predicate"`
		Object     string            `json:"object"`
		Qualifiers map[string]string `json:"qualifiers"`
		Confidence float64           `json:"confidence"`
	} `json:"claims"`
	Procedures []struct {
		Name    string `json:"name"`
		Trigger string `json:"trigger"`
		Steps   []struct {
			N        int    `json:"n"`
			Do       string `json:"do"`
			Owner    string `json:"owner"`
			Deadline string `json:"deadline"`
		} `json:"steps"`
	} `json:"procedures"`
}

// Proposal er det validerte uttrekket, klart til lagring. Navnene er beholdt
// (ikke id-er): oppslag mot eksisterende entiteter skjer i lagringssteget,
// der basen finnes.
type Proposal struct {
	Entities   []Entity
	Claims     []ProposedClaim
	Procedures []ProposedProcedure
	// Rejected forteller hva som ble kastet og hvorfor — grunnlaget for å se
	// om vokabularet mangler noe folk faktisk sier.
	Rejected []string
}

type ProposedClaim struct {
	Subject        string
	Predicate      string
	Object         string // entitetsnavn eller verdi, avgjort av predikatet
	ObjectIsEntity bool
	Qualifiers     map[string]string
	Confidence     float64
}

type ProposedProcedure struct {
	Name    string
	Trigger string
	Steps   []Step
	Owners  []string // navn, slås opp ved lagring
}

// ParseProposal leser modellsvaret og kaster alt som ikke holder mål.
func ParseProposal(raw []byte) (Proposal, error) {
	var r Raw
	// Modeller pakker gjerne JSON i ```json-blokker.
	clean := strings.TrimSpace(string(raw))
	if i := strings.Index(clean, "{"); i > 0 {
		clean = clean[i:]
	}
	if i := strings.LastIndex(clean, "}"); i >= 0 && i < len(clean)-1 {
		clean = clean[:i+1]
	}
	if err := json.Unmarshal([]byte(clean), &r); err != nil {
		return Proposal{}, fmt.Errorf("ugyldig uttrekk: %w", err)
	}

	var p Proposal
	known := map[string]string{} // navn (lowercase) → kind

	for _, e := range r.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		kind := strings.TrimSpace(strings.ToLower(e.Kind))
		if !ValidKind(kind) {
			p.Rejected = append(p.Rejected, fmt.Sprintf("entitet %q: ukjent type %q", name, e.Kind))
			continue
		}
		var aliases []string
		for _, a := range e.Aliases {
			if a = strings.TrimSpace(a); a != "" && !strings.EqualFold(a, name) {
				aliases = append(aliases, a)
			}
		}
		p.Entities = append(p.Entities, Entity{Kind: kind, Name: name, Aliases: aliases})
		known[strings.ToLower(name)] = kind
	}

	for _, c := range r.Claims {
		subj := strings.TrimSpace(c.Subject)
		obj := strings.TrimSpace(c.Object)
		pred, ok := Lookup(c.Predicate)
		if !ok {
			p.Rejected = append(p.Rejected, fmt.Sprintf("påstand %q: predikatet finnes ikke i vokabularet", c.Predicate))
			continue
		}
		if subj == "" || obj == "" {
			p.Rejected = append(p.Rejected, fmt.Sprintf("påstand %q: mangler subjekt eller objekt", c.Predicate))
			continue
		}
		if strings.EqualFold(subj, obj) {
			p.Rejected = append(p.Rejected, fmt.Sprintf("påstand %q peker på seg selv", c.Predicate))
			continue
		}
		// Subjektet MÅ være en entitet. Nevnes den ikke i entities-lista,
		// legger vi den til som person/entitet av ukjent type — heller det
		// enn å miste faktumet.
		if _, seen := known[strings.ToLower(subj)]; !seen {
			p.Entities = append(p.Entities, Entity{Kind: guessKind(subj), Name: subj})
			known[strings.ToLower(subj)] = "ukjent"
		}
		objIsEntity := pred.Object == "entity"
		if objIsEntity {
			if _, seen := known[strings.ToLower(obj)]; !seen {
				p.Entities = append(p.Entities, Entity{Kind: guessKind(obj), Name: obj})
				known[strings.ToLower(obj)] = "ukjent"
			}
		}
		conf := c.Confidence
		if conf <= 0 || conf > 1 {
			conf = 0.7 // modellen glemmer feltet oftere enn den bommer på det
		}
		p.Claims = append(p.Claims, ProposedClaim{
			Subject: subj, Predicate: pred.Key, Object: obj,
			ObjectIsEntity: objIsEntity, Qualifiers: cleanQualifiers(c.Qualifiers), Confidence: conf,
		})
	}

	for _, pr := range r.Procedures {
		name := strings.TrimSpace(pr.Name)
		if name == "" || len(pr.Steps) == 0 {
			continue
		}
		var steps []Step
		var owners []string
		for i, s := range pr.Steps {
			do := strings.TrimSpace(s.Do)
			if do == "" {
				continue
			}
			n := s.N
			if n <= 0 {
				n = i + 1
			}
			steps = append(steps, Step{N: n, Do: do, Deadline: strings.TrimSpace(s.Deadline)})
			if o := strings.TrimSpace(s.Owner); o != "" {
				owners = append(owners, o)
			}
		}
		if len(steps) == 0 {
			continue
		}
		p.Procedures = append(p.Procedures, ProposedProcedure{
			Name: name, Trigger: strings.TrimSpace(pr.Trigger), Steps: steps, Owners: owners,
		})
	}
	return p, nil
}

// guessKind gjetter grovt når modellen ikke oppga typen. Det er bedre enn å
// miste faktumet, og typen kan rettes i grafen.
func guessKind(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, " as") || strings.Contains(l, " asa") || strings.Contains(l, " ab"):
		return "kunde"
	case strings.HasSuffix(l, "avdelingen") || strings.HasSuffix(l, "teamet"):
		return "avdeling"
	case len(strings.Fields(name)) <= 2 && strings.Title(l) == name:
		return "person"
	default:
		return "person"
	}
}

// cleanQualifiers beholder bare korte, meningsbærende verdier.
func cleanQualifiers(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		k = strings.TrimSpace(strings.ToLower(k))
		v = strings.TrimSpace(v)
		if k == "" || v == "" || len([]rune(v)) > 60 {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Supersedes sier om en ny påstand erstatter en gjeldende: samme subjekt og
// predikat betyr at den gamle sluttet å gjelde. Uten dette ligger «vi bruker
// Neon» og «vi bruker Postgres» side om side, begge like sanne.
func Supersedes(newClaim Claim, existing Claim) bool {
	if newClaim.SubjectID != existing.SubjectID || newClaim.Predicate != existing.Predicate {
		return false
	}
	if existing.ValidTo != nil {
		return false
	}
	// Samme objekt er en gjentakelse, ikke en endring.
	if newClaim.ObjectID != "" && newClaim.ObjectID == existing.ObjectID {
		return false
	}
	if newClaim.ObjectText != "" && strings.EqualFold(newClaim.ObjectText, existing.ObjectText) {
		return false
	}
	// Predikater som naturlig har flere samtidige verdier skal ikke lukkes.
	return !multiValued(newClaim.Predicate)
}

// multiValued: relasjoner der flere svar kan være sanne samtidig.
func multiValued(predicate string) bool {
	switch predicate {
	case "samarbeider med", "har kunde", "er ansvarlig for", "møter",
		"leverer", "bruker", "gjelder for", "er kontakt for":
		return true
	}
	return false
}

// Now er tidskilden, skilt ut så tester kan styre den.
var Now = func() time.Time { return time.Now() }
