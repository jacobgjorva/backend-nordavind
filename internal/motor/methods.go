package motor

// Metodekatalogen — motorens eneste sted for oppgavespesifikk adferd.
//
// HVORFOR EN KATALOG. v1-v5 løste hver ny oppgavetype med en ny kodegren:
// en regex som gjenkjente spørsmålet, en funksjon som gjorde noe eget, en
// vakt som ryddet opp etterpå. Etter tjue slike var motoren umulig å endre
// uten å ødelegge noe annet. Her er en oppgaveklasse en RAD: en tekst som
// beskriver fremgangsmåten, og fire tall som beskriver hva den har råd til.
//
// SLIK JUSTERES MOTOREN, uten unntak:
//
//	Ny oppgaveklasse   → ny rad her + ny rad i intent-registeret.
//	Dypere/billigere   → endre et tall i radens Budget.
//	Annen svarform     → endre radens Text.
//	Feil klasse valgt  → nytt eksempel i intent-registeret.
//
// Ingen av delene er en kodegren. Er du fristet til å skrive en if-setning
// for et enkelttilfelle, er svaret nesten alltid en rad eller et tall her.
//
// TEKSTENE BESKRIVER MÅL, IKKE VERKTØY. Første versjon sa «les
// kandidatenes egne sider (fetch_url)». Modellen ignorerte det i samtlige
// målte turer — helt korrekt, for web_search henter ALLEREDE fire hele
// sider à 6 000 tegn (excerpt.go). Instruksen ba om en dublett. En metode
// som navngir et verktøy binder seg til dagens verktøyoppsett; en metode
// som sier hva som skal være SANT om svaret overlever at verktøyene endres.
//
// TEKSTENE ER MÅLT, IKKE SKREVET. Hver er probet mot faktisk
// mistral-large-2512 med ekte websøk over flere runder (v3/v4 —
// docs/MOTOR-V6-PROBER.md). De sier FREMGANGSMÅTE, aldri hva svaret skal
// inneholde av innhold: ingen bransje, ingen entitet, ingen eksempler.
// Et eksempel i en metodetekst er overtilpasning i det øyeblikket det
// skrives.
//
// KUN ÉN TEKST PER TUR. Stabling av regler er målt å gjøre svarene verre
// (feedback-no-prompt-stuffing), så klassene er gjensidig utelukkende og
// Turn.Method bærer nøyaktig én.

// Method er én oppgaveklasse.
type Method struct {
	// Key er stabil identifikator, brukt i logg og ruting.
	Key MethodKey
	// Text er fremgangsmåten som injiseres i systemprompten for DENNE turen.
	// Starter med mellomrom: den appendes til de modulære reglene.
	Text string
	// Budget er hva klassen har råd til. Kode håndhever; modellen teller aldri.
	Budget Budget
}

// Metodenøklene. Verdiene er stabile og logges.
const (
	MethodLookup    MethodKey = "oppslag"
	MethodRelation  MethodKey = "relasjon"
	MethodAdvice    MethodKey = "anbefaling"
	MethodAnalysis  MethodKey = "analyse"
	MethodCreative  MethodKey = "skapende"
	MethodSmalltalk MethodKey = "samtale"
)

// Catalog er alle klassene motoren kjenner.
//
// MaxChars=0 betyr «bruk flytens egen grense» (intent/flows.go). Kun
// klassene der svarkontrakten trenger mer plass enn flyten gir, setter et
// eget tak — research-svaret har tre obligatoriske deler, og et trangt
// budsjett amputerte dem i probene.
var Catalog = map[MethodKey]Method{
	MethodLookup: {
		Key: MethodLookup,
		// Ett søk, ingen sidehenting: probene (o1, o2) landet korrekt svar med
		// dato på to runder. Trenger en klasse dypere lesing, er det ett tall
		// som endres — ikke en ny gren.
		Budget: Budget{Searches: 1, Fetches: 0, Rounds: 2},
		Text: " METODE: Dette er et faktaoppslag. Verifiser med ett søk selv om du tror du vet svaret — " +
			"ferskhet slår hukommelse. Én autoritativ kilde holder. Svar med faktumet og tidspunktet det " +
			"gjelder for. Ikke utred.",
	},

	MethodRelation: {
		Key: MethodRelation,
		// Den dyreste klassen, med vilje: profilering FØR kandidatsøk er hele
		// poenget, og profilen ligger i sider som må leses hele.
		Budget: Budget{Searches: 4, Fetches: 4, Rounds: 6, MaxChars: 900},
		Text: " METODE: Svaret avhenger av hvem subjektet ER. Arbeidsrekkefølge: (1) Profiler subjektet " +
			"fra nærmeste kilde — det brukeren har fortalt, og aktørens egne sider når de finnes. (2) Si i " +
			"arbeidsnotatet hvilket kriterium relasjonen krever her, utledet av det brukeren skal beslutte. " +
			"(3) Let etter kandidater INNENFOR det segmentet, og bygg dommen om portefølje eller posisjon " +
			"på KILDETEKSTEN du får tilbake — aldri på titler, topplister eller din egen hukommelse. " +
			"Mangler en avgjørende kilde, hent den siden særskilt. " +
			"SVARKONTRAKT, alle tre delene: (a) kandidatene i løpende tekst med hver sin korte begrunnelse " +
			"mot kriteriet; (b) én setning som avgrenser mot aktørene som IKKE kvalifiserer — navngi kun " +
			"aktører som står i kildene eller samtalen, ellers beskriv kategorien uten navn; (c) hvis " +
			"profilen har et konkret hull som begrenser svaret: avslutt med ETT spørsmål om akkurat det " +
			"hullet — dette spørsmålet er en del av leveransen, ikke vegring. " +
			"Tall og fakta om kandidater KUN ordrett fra kildene. Verifiser til slutt påstanden som ville endret brukerens beslutning hvis den er feil: ett målrettet søk mot aktørens egen side, ikke omtaler — rett eller stryk det som ikke stemmer.",
	},

	MethodAdvice: {
		Key:    MethodAdvice,
		Budget: Budget{Searches: 3, Fetches: 4, Rounds: 6, MaxChars: 700},
		Text: " METODE: Etabler behovet fra samtalen før du leter. Søk etter kategorien og segmentet, " +
			"aldri «beste X»-fraser — topplister er ikke evidens. Bygg anbefalingen på KILDETEKSTEN om " +
			"leverandøren, ikke på titler eller hukommelse; mangler du leverandørens egen side, hent den. " +
			"Lever ÉN anbefaling: hvorfor den passer akkurat denne " +
			"situasjonen, med pris og innhold KUN ordrett fra kilden — har du ikke lest prisen, ikke oppgi " +
			"den; si hvor den finnes. Avslutt med den ENE beslutningsregelen som ville snudd valget: bygger " +
			"den på noe kildene sier, si det; ellers merk den som din vurdering. Aldri en meny, aldri " +
			"diktede terskler. Verifiser til slutt påstanden som ville endret brukerens beslutning hvis den er feil: ett målrettet søk mot aktørens egen side, ikke omtaler — rett eller stryk det som ikke stemmer. " +
			"Ber brukeren eksplisitt om svar uten research: svar kort, men si at det er " +
			"fra hukommelsen og uverifisert, og tilby å sjekke — presenter aldri uverifisert som fakta.",
	},

	MethodAnalysis: {
		Key: MethodAnalysis,
		// Databasen har ingen kvote (den er billig og lokal); nettet er
		// støtten for hybride spørsmål (interne tall × ekstern virkelighet).
		Budget: Budget{Searches: 2, Fetches: 1, Rounds: 6},
		Text: " METODE: Hent dataene med verktøyene; koden regner statistikk du skal bruke ordrett. " +
			"Skil tre ting i svaret: hva tallene viser, hva de ikke viser, og hva som ville endret " +
			"konklusjonen. En påstand om sammenheng eller forskjell tallfester BEGGE sidene den " +
			"sammenligner. Finner du ikke formen du ønsket, bruk nærmeste form som FINNES og si i én " +
			"bisetning hva du brukte.",
	},

	MethodCreative: {
		Key: MethodCreative,
		// Påfunn ER leveransen: kildekontroll på diktede navn er å sabotere
		// jobben (målt — «her stopper jeg» midt i en navneliste).
		Budget: Budget{Searches: 0, Fetches: 0, Rounds: 1},
		Text: " METODE: Påfunn er leveransen. Lever ferdig arbeid med ett tydelig førstevalg, aldri " +
			"prosess. Her er varianter lov.",
	},

	MethodSmalltalk: {
		Key:    MethodSmalltalk,
		Budget: Budget{Searches: 0, Fetches: 0, Rounds: 1},
		Text: " METODE: Ingen verktøy trengs. Svar kort med husets stemme. Er det et reelt behov bak " +
			"småpraten, pek på det i én setning.",
	},
}

// flowMethod knytter en rutet flyt til sin metodeklasse.
//
// Flyter som IKKE står her får MethodNone — naken løkke, dagens adferd.
// Det er den trygge standarden: en flyt uten bevist metode skal aldri
// arve en fremmed fremgangsmåte bare fordi den lignet.
var flowMethod = map[string]MethodKey{
	"research_relation": MethodRelation,
	"recommendation":    MethodAdvice,
	"web_fact":          MethodLookup,
	"data_question":     MethodAnalysis,
	"smalltalk":         MethodSmalltalk,
}

// For gir metoden for en flyt. Ukjent flyt, tom flyt eller flyt uten
// metode → MethodNone, som betyr naken løkke uten metodetekst.
func For(flowKey string) MethodKey {
	return flowMethod[flowKey]
}

// Get henter raden. ok=false for MethodNone og for ukjente nøkler; kalleren
// kjører da uten metodetekst og med standardbudsjettet.
func Get(k MethodKey) (Method, bool) {
	m, ok := Catalog[k]
	return m, ok
}

// DefaultBudget gjelder turer uten metode (naken løkke). Tallene er dagens
// tak i legacy-motoren, så en tur uten metode oppfører seg nøyaktig som i
// dag — fail-open er aldri en oppgradering.
var DefaultBudget = Budget{Searches: 4, Fetches: 3, Rounds: 6}

// MaxBudget er det absolutte kostnadstaket. En metode kan være DYPERE enn
// standarden der det er målt nødvendig (relasjonsresearch leste fire
// kandidatsider i proben, og det var nettopp dét som gjorde svaret godt) —
// men ingen rad får sette et tall som gjør en tur uforsvarlig dyr.
// Vakten er strukturell: den fanger tastefeil og glidning, ikke skjønn.
var MaxBudget = Budget{Searches: 6, Fetches: 6, Rounds: 8, MaxChars: 1200}

// BudgetFor gir budsjettet turen skal kjøre på.
func BudgetFor(k MethodKey) Budget {
	if m, ok := Get(k); ok {
		return m.Budget
	}
	return DefaultBudget
}

// TextFor gir metodeteksten som skal injiseres, eller tom streng.
func TextFor(k MethodKey) string {
	if m, ok := Get(k); ok {
		return m.Text
	}
	return ""
}
