package brain

import (
	"strings"
	"testing"
	"time"
)

// Vokabularet er kontrakten mellom kode og modell. Ryker den, blir grafen
// usøkbar — derfor holdes den fast av en test, ikke av disiplin.
func TestVocabularyIsSound(t *testing.T) {
	if len(Predicates()) < 10 || len(Kinds()) < 5 {
		t.Fatal("vokabularet ser tomt ut")
	}
	for _, p := range Predicates() {
		if p.Inverse == "" {
			continue
		}
		inv, ok := Lookup(p.Inverse)
		if !ok {
			t.Errorf("%s peker på ukjent invers %q", p.Key, p.Inverse)
			continue
		}
		if inv.Inverse != p.Key {
			t.Errorf("%s ↔ %s er ikke gjensidige", p.Key, inv.Key)
		}
	}
	cat := Catalog()
	for _, p := range Predicates() {
		if !strings.Contains(cat, p.Key) {
			t.Errorf("katalogen mangler %q", p.Key)
		}
	}
}

func TestValidateRejectsBadClaims(t *testing.T) {
	base := Claim{SubjectID: "a", ObjectID: "b", Predicate: "har sjef",
		Confidence: 1, SourceKind: "chat", SourceID: "c1"}
	if err := base.Validate(); err != nil {
		t.Fatalf("gyldig påstand ble avvist: %v", err)
	}
	for name, c := range map[string]Claim{
		"ukjent predikat":   {SubjectID: "a", ObjectID: "b", Predicate: "liker", Confidence: 1, SourceKind: "chat", SourceID: "c"},
		"uten subjekt":      {ObjectID: "b", Predicate: "har sjef", Confidence: 1, SourceKind: "chat", SourceID: "c"},
		"uten kilde":        {SubjectID: "a", ObjectID: "b", Predicate: "har sjef", Confidence: 1},
		"peker på seg selv": {SubjectID: "a", ObjectID: "a", Predicate: "har sjef", Confidence: 1, SourceKind: "chat", SourceID: "c"},
		"verdi der entitet kreves": {SubjectID: "a", ObjectText: "Kari", Predicate: "har sjef",
			Confidence: 1, SourceKind: "chat", SourceID: "c"},
		"entitet der verdi kreves": {SubjectID: "a", ObjectID: "b", Predicate: "har frist",
			Confidence: 1, SourceKind: "chat", SourceID: "c"},
		"sikkerhet utenfor skala": {SubjectID: "a", ObjectID: "b", Predicate: "har sjef",
			Confidence: 2, SourceKind: "chat", SourceID: "c"},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: skulle vært avvist", name)
		}
	}
}

// testgraf: Jacob har sjef Kari, og møter Kari ukentlig på mandag.
// De to fakta har ALDRI stått i samme setning.
func testGraph(t *testing.T) Graph {
	t.Helper()
	now := time.Now()
	ents := map[string]Entity{
		"e1": {ID: "e1", Kind: "person", Name: "Jacob"},
		"e2": {ID: "e2", Kind: "person", Name: "Kari", Aliases: []string{"sjefen"}},
		"e3": {ID: "e3", Kind: "kunde", Name: "Vestland Fisk"},
	}
	claims := []Claim{
		{ID: "c1", SubjectID: "e1", Predicate: "har sjef", ObjectID: "e2",
			Confidence: 0.9, ValidFrom: now.Add(-72 * time.Hour), SourceKind: "chat", SourceID: "s1"},
		{ID: "c2", SubjectID: "e1", Predicate: "møter", ObjectID: "e2",
			Qualifiers: map[string]string{"frekvens": "ukentlig", "dag": "mandag"},
			Confidence: 0.8, ValidFrom: now.Add(-48 * time.Hour), SourceKind: "chat", SourceID: "s2"},
		{ID: "c3", SubjectID: "e2", Predicate: "er ansvarlig for", ObjectID: "e3",
			Confidence: 0.7, ValidFrom: now.Add(-24 * time.Hour), SourceKind: "chat", SourceID: "s3"},
	}
	for _, c := range claims {
		if err := c.Validate(); err != nil {
			t.Fatalf("testdata er ugyldige: %v", err)
		}
	}
	return Graph{Entities: ents, Claims: claims}
}

// Selve poenget med hjernen: «hvilken dag har jeg møte med sjefen min?»
// besvares ved å koble to påstander som aldri sto sammen.
func TestInferenceAcrossTwoClaims(t *testing.T) {
	g := testGraph(t)
	now := time.Now()

	sjefer := g.Resolve("e1", "har sjef", now)
	if len(sjefer) != 1 || sjefer[0].Name != "Kari" {
		t.Fatalf("fant ikke sjefen: %+v", sjefer)
	}
	var dag string
	for _, c := range g.Neighbors("e1", now) {
		if c.Predicate == "møter" && c.ObjectID == sjefer[0].ID {
			dag = c.Qualifiers["dag"]
		}
	}
	if dag != "mandag" {
		t.Errorf("koblet ikke møtet til sjefen, fikk dag=%q", dag)
	}
}

// Inversen skal virke uten at samme faktum lagres to ganger.
func TestResolveFollowsInverse(t *testing.T) {
	g := testGraph(t)
	under := g.Resolve("e2", "er sjef for", time.Now())
	if len(under) != 1 || under[0].Name != "Jacob" {
		t.Errorf("inversen virket ikke: %+v", under)
	}
}

// To hopp: Jacob → Kari → Vestland Fisk. Ingen enkelt påstand sier at Jacobs
// sjef har ansvar for den kunden.
func TestExpandReachesTwoHops(t *testing.T) {
	g := testGraph(t)
	paths := g.Expand([]string{"e1"}, 2, time.Now())
	var reached bool
	for _, p := range paths {
		if p.Hops == 2 && last(p.Claims).ObjectID == "e3" {
			reached = true
			if len(p.Claims) != 2 {
				t.Errorf("stien mangler mellomleddet: %+v", p.Claims)
			}
		}
	}
	if !reached {
		t.Error("nådde ikke kunden på to hopp")
	}
	// Nærmest først: rekkefølgen er det som avgjør hva som får plass i
	// konteksten når budsjettet er trangt.
	if paths[0].Hops != 1 {
		t.Errorf("stiene er ikke sortert etter avstand: %+v", paths[0])
	}
}

// Utdaterte påstander skal ALDRI dras med i traverseringen.
func TestExpiredClaimsAreInvisible(t *testing.T) {
	g := testGraph(t)
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	for i := range g.Claims {
		if g.Claims[i].ID == "c1" {
			g.Claims[i].ValidTo = &past
		}
	}
	if got := g.Resolve("e1", "har sjef", now); len(got) != 0 {
		t.Errorf("lukket påstand ble brukt likevel: %+v", got)
	}
}

// Konteksten modellen får skal være kort og bære kvalifikatorene.
func TestRenderIsCompactAndKeepsQualifiers(t *testing.T) {
	g := testGraph(t)
	out := g.Render(g.Expand([]string{"e1"}, 2, time.Now()), 10)
	if !strings.Contains(out, "Jacob møter Kari") || !strings.Contains(out, "dag: mandag") {
		t.Errorf("linjene mangler innhold: %q", out)
	}
	if n := strings.Count(out, "\n"); n > 6 {
		t.Errorf("konteksten er for lang: %d linjer", n)
	}
}
