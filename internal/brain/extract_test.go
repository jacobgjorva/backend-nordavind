package brain

import (
	"strings"
	"testing"
	"time"
)

// Uttrekket er hjernens inngangsport. Alt som slipper gjennom her, tror
// systemet på siden — derfor er testene her strengest.

func TestParseProposalBuildsClaimsAndEntities(t *testing.T) {
	raw := []byte(`{
	  "entities":[
	    {"name":"Jacob","kind":"person"},
	    {"name":"Kari Nes","kind":"person","aliases":["sjefen","KN"]}
	  ],
	  "claims":[
	    {"subject":"Jacob","predicate":"har sjef","object":"Kari Nes","confidence":0.9},
	    {"subject":"Jacob","predicate":"møter","object":"Kari Nes",
	     "qualifiers":{"frekvens":"ukentlig","dag":"mandag"},"confidence":0.85}
	  ]}`)
	p, err := ParseProposal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entities) != 2 || len(p.Claims) != 2 {
		t.Fatalf("forventet 2 entiteter og 2 påstander, fikk %d/%d", len(p.Entities), len(p.Claims))
	}
	if p.Claims[1].Qualifiers["dag"] != "mandag" {
		t.Errorf("kvalifikatoren gikk tapt: %+v", p.Claims[1].Qualifiers)
	}
	if !p.Claims[0].ObjectIsEntity {
		t.Error("«har sjef» skal ha entitet som objekt")
	}
	var kari Entity
	for _, e := range p.Entities {
		if e.Name == "Kari Nes" {
			kari = e
		}
	}
	if len(kari.Aliases) != 2 {
		t.Errorf("aliasene forsvant: %+v", kari.Aliases)
	}
}

// Modellen finner på predikater. Da skal faktumet kastes — og det skal
// FORTELLES hvorfor, så vokabularet kan utvides bevisst.
func TestParseProposalRejectsUnknownPredicate(t *testing.T) {
	raw := []byte(`{"claims":[
	  {"subject":"Jacob","predicate":"liker","object":"kaffe"},
	  {"subject":"Jacob","predicate":"har sjef","object":"Kari"}]}`)
	p, err := ParseProposal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Claims) != 1 || p.Claims[0].Predicate != "har sjef" {
		t.Fatalf("feil påstand slapp gjennom: %+v", p.Claims)
	}
	if len(p.Rejected) != 1 || !strings.Contains(p.Rejected[0], "liker") {
		t.Errorf("avvisningen ble ikke rapportert: %+v", p.Rejected)
	}
}

// Subjekt og objekt som ikke står i entities-lista skal likevel bli entiteter
// — ellers mister vi faktumet fordi modellen var slurvete.
func TestParseProposalInfersMissingEntities(t *testing.T) {
	raw := []byte(`{"claims":[{"subject":"Ola","predicate":"er ansvarlig for","object":"Vestland AS"}]}`)
	p, err := ParseProposal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entities) != 2 {
		t.Fatalf("forventet to utledede entiteter, fikk %+v", p.Entities)
	}
	if len(p.Claims) != 1 {
		t.Fatalf("påstanden gikk tapt: %+v", p.Claims)
	}
}

// Verdi-predikater skal ikke lage entiteter av verdien: «48 timer» er ikke en
// ting i grafen.
func TestParseProposalKeepsValuesOutOfTheGraph(t *testing.T) {
	raw := []byte(`{"entities":[{"name":"Klarering","kind":"prosedyre"}],
	 "claims":[{"subject":"Klarering","predicate":"har frist","object":"48 timer før levering"}]}`)
	p, err := ParseProposal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entities) != 1 {
		t.Errorf("verdien ble en entitet: %+v", p.Entities)
	}
	if p.Claims[0].ObjectIsEntity {
		t.Error("«har frist» skal ta en verdi, ikke en entitet")
	}
}

// En fremgangsmåte skal bli prosedyre med rekkefølgen intakt.
func TestParseProposalKeepsProcedureOrder(t *testing.T) {
	raw := []byte(`{"procedures":[{"name":"Eksport til EU","trigger":"ved levering til EU",
	 "steps":[{"n":1,"do":"Bestill helsesertifikat","owner":"Ola","deadline":"48 t før"},
	          {"n":2,"do":"Meld inn til Mattilsynet"},
	          {"n":3,"do":"Send papirene til speditør"}]}]}`)
	p, err := ParseProposal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Procedures) != 1 {
		t.Fatalf("prosedyren forsvant: %+v", p.Procedures)
	}
	pr := p.Procedures[0]
	if len(pr.Steps) != 3 || pr.Steps[0].N != 1 || pr.Steps[2].N != 3 {
		t.Fatalf("rekkefølgen er ødelagt: %+v", pr.Steps)
	}
	if pr.Steps[0].Deadline != "48 t før" || len(pr.Owners) != 1 {
		t.Errorf("frist eller ansvarlig gikk tapt: %+v", pr)
	}
}

// Modeller pakker JSON i kodeblokker. Det skal ikke velte uttrekket.
func TestParseProposalHandlesFencedJSON(t *testing.T) {
	raw := []byte("Her er resultatet:\n```json\n{\"claims\":[{\"subject\":\"A\",\"predicate\":\"har sjef\",\"object\":\"B\"}]}\n```")
	p, err := ParseProposal(raw)
	if err != nil {
		t.Fatalf("klarte ikke lese innpakket JSON: %v", err)
	}
	if len(p.Claims) != 1 {
		t.Errorf("fant ikke påstanden: %+v", p.Claims)
	}
}

// Tidslinjen: et nytt svar på samme spørsmål lukker det gamle, men flere
// samtidige kunder eller ansvar skal leve side om side.
func TestSupersedesOnlyForSingleValuedPredicates(t *testing.T) {
	now := time.Now()
	old := Claim{SubjectID: "e1", Predicate: "har sjef", ObjectID: "e2", ValidFrom: now.Add(-time.Hour)}
	nyy := Claim{SubjectID: "e1", Predicate: "har sjef", ObjectID: "e3", ValidFrom: now}
	if !Supersedes(nyy, old) {
		t.Error("ny sjef skulle lukket den gamle påstanden")
	}
	if Supersedes(Claim{SubjectID: "e1", Predicate: "har sjef", ObjectID: "e2"}, old) {
		t.Error("samme objekt er en gjentakelse, ikke en endring")
	}
	multi := Claim{SubjectID: "e1", Predicate: "er ansvarlig for", ObjectID: "e2", ValidFrom: now.Add(-time.Hour)}
	if Supersedes(Claim{SubjectID: "e1", Predicate: "er ansvarlig for", ObjectID: "e3"}, multi) {
		t.Error("man kan være ansvarlig for flere ting samtidig")
	}
	closed := old
	end := now
	closed.ValidTo = &end
	if Supersedes(nyy, closed) {
		t.Error("en allerede lukket påstand skal ikke lukkes igjen")
	}
}

// Prompten må alltid bære det gjeldende vokabularet — ellers finner modellen
// på predikater vi kaster rett etterpå.
func TestPromptCarriesVocabulary(t *testing.T) {
	p := Prompt()
	for _, pred := range Predicates() {
		if !strings.Contains(p, pred.Key) {
			t.Fatalf("prompten mangler predikatet %q", pred.Key)
		}
	}
}
