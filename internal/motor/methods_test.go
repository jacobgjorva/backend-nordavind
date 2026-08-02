package motor

import (
	"strings"
	"testing"
)

// Katalogen er data, så testene her er STRUKTURELLE: de håndhever
// egenskapene som gjør data trygt å endre, aldri innholdet i en enkelt
// tekst. En test som krevde en bestemt formulering ville gjort katalogen
// like stiv som kodegrenene den erstattet.

// Ingen metodetekst får inneholde et eksempel: en bransje, et selskap eller
// en konkret formulering fra et testtilfelle. Det er overtilpasning i det
// øyeblikket det skrives, og har felt fire tidligere motorer.
func TestMethodTextsCarryNoExamples(t *testing.T) {
	// Ord som bare kan komme fra et konkret testtilfelle eller en bransje.
	banned := []string{
		"blend", "wines", "vin", "regnskapsbyrå", "tromsø", "bølgepapp",
		"fiskeri", "oppdrett", "hms", "crm", "erp", "styringsrenten",
		"konkurrent", "vinmonopolet", "horeca",
	}
	for key, m := range Catalog {
		low := strings.ToLower(m.Text)
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("%s: metodeteksten nevner «%s» — eksempler hører aldri i en metode", key, b)
			}
		}
	}
}

// Tekstene skal være fremgangsmåte i komprimert form. For korte sier de
// ingenting; for lange konkurrerer de med kjernereglene (målt: stabling
// gjør svarene verre).
func TestMethodTextsAreBounded(t *testing.T) {
	for key, m := range Catalog {
		n := len([]rune(m.Text))
		if n < 100 {
			t.Errorf("%s: teksten er %d tegn — for tynn til å styre noe", key, n)
		}
		// Taket ble hevet fra 1200 da premissjekk-regelen målte inn
		// (kjøring 17): proben kjørte metodetekst + regel samtidig, så
		// samlet last på denne størrelsen er validert mot ekte modell.
		if n > 1800 {
			t.Errorf("%s: teksten er %d tegn — for tung, konkurrerer med kjernereglene", key, n)
		}
		if !strings.HasPrefix(m.Text, " METODE:") {
			t.Errorf("%s: teksten må starte med « METODE:» (mellomrom foran, appendes til modulreglene)", key)
		}
	}
}

// Nøkkelen i kartet og i raden må være samme; ellers logges feil klasse.
func TestCatalogKeysMatchRows(t *testing.T) {
	for key, m := range Catalog {
		if m.Key != key {
			t.Errorf("kartnøkkel %q peker på rad med Key %q", key, m.Key)
		}
		if key == MethodNone {
			t.Error("MethodNone er fravær av metode og skal aldri stå i katalogen")
		}
	}
}

// Hver rad må ha et budsjett som faktisk begrenser noe. Et glemt budsjett
// (alle nuller på en klasse som skal hente) ville gjort verktøyene stille
// utilgjengelige.
func TestEveryMethodHasCoherentBudget(t *testing.T) {
	needsTools := map[MethodKey]bool{
		MethodLookup: true, MethodRelation: true, MethodAdvice: true, MethodAnalysis: true,
		MethodAdvisory: true, MethodGround: true,
	}
	for key, m := range Catalog {
		b := m.Budget
		if b.Rounds < 1 {
			t.Errorf("%s: rundetak %d — turen kan aldri kjøre", key, b.Rounds)
		}
		if b.Searches < 0 || b.Fetches < 0 || b.MaxChars < 0 {
			t.Errorf("%s: negativt budsjett %+v", key, b)
		}
		if needsTools[key] && b.Searches == 0 {
			t.Errorf("%s henter evidens, men har null søk", key)
		}
		if !needsTools[key] && (b.Searches > 0 || b.Fetches > 0) {
			t.Errorf("%s skal ikke hente, men har søk/hentinger: %+v", key, b)
		}
		// Fetch uten search er ufullstendig: modellen får ikke en URL å
		// hente uten å ha søkt først.
		if b.Fetches > 0 && b.Searches == 0 {
			t.Errorf("%s kan hente sider, men aldri finne dem", key)
		}
	}
}

// Rutingen må peke på klasser som finnes, og hver rutbar flyt må gi
// nøyaktig én metode.
func TestFlowMappingResolvesToOneKnownMethod(t *testing.T) {
	for flow, key := range flowMethod {
		if _, ok := Get(key); !ok {
			t.Errorf("flyt %q peker på ukjent metode %q", flow, key)
		}
		if got := For(flow); got != key {
			t.Errorf("flyt %q: For ga %q, tabellen sier %q", flow, got, key)
		}
	}
	// Fail-open gjelder UKJENTE flyter. free_chat/uklart er bevisst flyttet
	// til grunnmetoden (2026-08-02): all målt prod-fabrikasjon bodde i den
	// nakne løkka, så «ingen tur uten metode» er nå kontrakten for samtale.
	for _, unknown := range []string{"", "m365_files", "finnes_ikke"} {
		if For(unknown) != MethodNone {
			t.Errorf("flyt %q skal falle til naken løkke, fikk %q", unknown, For(unknown))
		}
	}
	for _, ground := range []string{"free_chat", "uklart"} {
		if For(ground) != MethodGround {
			t.Errorf("flyt %q skal ha grunnmetoden, fikk %q", ground, For(ground))
		}
	}
}

// Katalogklasser uten rutingsvei er dokumentert her, ikke glemt. En ny
// klasse som aldri kan velges er død data.
func TestUnroutableMethodsAreDeclared(t *testing.T) {
	pending := map[MethodKey]string{
		MethodCreative: "venter på en egen rutingsklasse; kreative bestillinger går i dag gjennom fri chat",
	}
	routed := map[MethodKey]bool{}
	for _, key := range flowMethod {
		routed[key] = true
	}
	for key := range Catalog {
		if routed[key] {
			if _, listed := pending[key]; listed {
				t.Errorf("%s er rutbar nå — fjern den fra pending-lista", key)
			}
			continue
		}
		if _, listed := pending[key]; !listed {
			t.Errorf("%s kan ikke velges av noen flyt og er ikke dokumentert som pending", key)
		}
	}
}

// Fallback-oppslagene må være trygge for MethodNone: en tur uten metode
// skal få dagens tak, ikke null.
func TestFallbacksForMethodNone(t *testing.T) {
	if TextFor(MethodNone) != "" {
		t.Error("MethodNone skal ikke injisere tekst")
	}
	if got := BudgetFor(MethodNone); got != DefaultBudget {
		t.Errorf("MethodNone skal få standardbudsjettet, fikk %+v", got)
	}
	if got := BudgetFor("finnes_ikke"); got != DefaultBudget {
		t.Errorf("ukjent nøkkel skal få standardbudsjettet, fikk %+v", got)
	}
	// Standardbudsjettet MÅ speile legacys tak. Er de ulike, endrer en tur
	// uten metode oppførsel bare av å slå på motoren — og da måler A/B-en
	// noe annet enn metodene.
	if DefaultBudget != (Budget{Searches: 4, Fetches: 3, Rounds: 6}) {
		t.Errorf("standardbudsjettet er ikke lenger legacys tak (4/3/6): %+v", DefaultBudget)
	}
}

// Kostnadstaket er strukturvakten mot tastefeil og gradvis glidning. En
// metode har LOV til å være dypere enn standarden der det er målt (relasjon
// leser fire kandidatsider), men ingen rad får gjøre en tur uforsvarlig dyr.
func TestNoMethodExceedsHardCeiling(t *testing.T) {
	for key, m := range Catalog {
		b := m.Budget
		if b.Searches > MaxBudget.Searches || b.Fetches > MaxBudget.Fetches ||
			b.Rounds > MaxBudget.Rounds || b.MaxChars > MaxBudget.MaxChars {
			t.Errorf("%s sprenger kostnadstaket %+v: %+v", key, MaxBudget, b)
		}
	}
}

// Klassene er gjensidig utelukkende: nøyaktig én tekst per tur. Denne
// testen fester egenskapen strukturelt (Turn bærer én nøkkel), så ingen
// senere endring kan begynne å stable metoder.
func TestExactlyOneMethodPerTurn(t *testing.T) {
	turn := &Turn{Method: For("research_relation")}
	if turn.Method != MethodRelation {
		t.Fatalf("forventet %q, fikk %q", MethodRelation, turn.Method)
	}
	if TextFor(turn.Method) == "" {
		t.Error("valgt metode må ha tekst")
	}
	// Bytte klasse erstatter, aldri legger til.
	turn.Method = For("web_fact")
	if TextFor(turn.Method) != Catalog[MethodLookup].Text {
		t.Error("metodebytte skal erstatte teksten helt")
	}
}
