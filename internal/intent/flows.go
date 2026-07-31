package intent

// Flow er kontrakten for én flyt: hva modellen SER (Tools), hvilken modell som
// kjører (Model), hvor langt et svar har lov å bli før det skrives om i kode
// (MaxChars — mykt: aldri kutt, alltid omskriving til hele setninger), om
// flyten løses helt uten modell (Deterministic), hva som skjer ved feil
// (Fallback — alltid fri chat i v1) og om flyten henger igjen over flere
// meldinger (Sticky). Ingen prompt-felter — formulering styres av few-shots
// og grenser, aldri instrukser per flyt.
type Flow struct {
	// Tools er navnene på verktøyene modellen får i denne flyten. Tom liste =
	// ingen verktøy (rent samtalesvar). Navnene mappes til faktiske
	// verktøydefinisjoner ved integrasjon (ukjent navn = programmeringsfeil
	// som fanges av testen under).
	Tools []string
	// Model er modellnivået flyten kjører på ("mid" | "heavy" | "top").
	// Mappes til router-konstantene ved integrasjon.
	Model string
	// MaxChars er den myke svargrensen. 0 = ingen grense (deterministiske
	// flyter og flyter med eget visningsformat).
	MaxChars int
	// Deterministic markerer at flyten håndteres i kode uten modellkall
	// (f.eks. rendre et panel). Tools/Model/MaxChars ignoreres da.
	Deterministic bool
	// Fallback er flyten som tar over ved feil. I v1 alltid "free_chat".
	Fallback string
	// Knowledge: intern kunnskap (kunnskapsgrafen) injiseres i denne flyten.
	// Flyter om verden utenfor bedriften (web_fact) og ren prat (smalltalk)
	// får aldri interne lapper — det er bare tokens (G3 i docs/KNOWLEDGE.md).
	Knowledge bool

	// Sticky: flyten forblir aktiv for påfølgende meldinger til den avsluttes
	// eksplisitt (som widget-redigering i dag).
	Sticky bool

	// ExplicitOnly: flyten kan KUN utløses av en eksplisitt handling i
	// klienten (slash-kommando, fildropp) — aldri av chatteksten. Rutingen
	// demoterer slike treff til fri chat (Jacobs beslutning 2026-07-31:
	// «tokens» i en melding skal aldri åpne forbrukspanelet). Slash-veien
	// påvirkes ikke — den lever i frontend og går aldri gjennom motoren.
	ExplicitOnly bool
}

// Verktøynavn — én kilde til sannhet for flyt-tabellen. Skal matche navnene i
// verktøydefinisjonene ved integrasjon (sjekkes av TestFlowToolsExist).
const (
	ToolWebSearch     = "web_search"
	ToolFetchURL      = "fetch_url"
	ToolQueryDatabase = "query_database"
	ToolShowTable     = "show_table"
	ToolM365Search    = "m365_search"
	ToolM365Read      = "m365_read"
	ToolMailSearch    = "mail_search"
	ToolMailRead      = "mail_read"
	ToolMailCompose   = "mail_compose"
	ToolContactPerson = "contact_person"
	ToolSetWidget     = "set_widget"
	ToolCompose       = "compose"
	ToolPatch         = "patch"
	ToolRestyle       = "restyle"
	ToolConnectDB     = "connect_database"
	ToolConnectM365   = "connect_m365"
	ToolCheckM365     = "check_m365"
	ToolSaveM365App   = "save_m365_app"
	ToolUpdateAgent   = "update_agent"
	ToolSetupRoutine  = "setup_routine"
	ToolListAgents    = "list_agents"
)

// FreeChatKey er den bredeste flyten: dagens chatoppførsel, men med samme
// grense- og modellkontrakt som alt annet. Motoren ruter hit ved MethodNone,
// ved sammensatte ønsker og som Fallback for alle flyter.
const FreeChatKey = "free_chat"

// Flows er flyt-tabellen. VEDLIKEHOLD: nye behov = ny rad (og ny intent i
// registeret) — aldri spesialtilfelle-logikk utenfor tabellen.
var Flows = map[string]Flow{
	FreeChatKey: {
		Knowledge: true,
		Tools: []string{ToolWebSearch, ToolFetchURL, ToolQueryDatabase, ToolShowTable,
			ToolM365Search, ToolM365Read, ToolContactPerson},
		Model:    "mid",
		MaxChars: 700,
		Fallback: "", // fri chat er selv bunnen — ingen videre fallback
	},

	"connect_database": {
		// Deterministisk oppskrift-steg: koden spawner credential-skjemaet
		// direkte (se credentialBlock) — modellen ber aldri om passord.
		ExplicitOnly:  true,
		Deterministic: true,
		Fallback:      FreeChatKey,
	},
	"connect_m365": {
		// Deterministisk oppskrift-steg: koden velger riktig steg (koblet/
		// innlogging/app-registrering) — se m365SetupBlock.
		ExplicitOnly:  true,
		Deterministic: true,
		Fallback:      FreeChatKey,
	},
	"manage_connections": {
		ExplicitOnly:  true,
		Deterministic: true, // rendrer /tilkoblinger-panelet
		Fallback:      FreeChatKey,
	},

	"create_widget": {
		Knowledge: true,
		Tools:     []string{ToolSetWidget},
		Model:     "mid",
		MaxChars:  120, // widgeten ER svaret; teksten er kun kvittering
		Fallback:  FreeChatKey,
		Sticky:    true, // videre meldinger redigerer samme widget
	},
	"edit_widget": {
		Knowledge: true,
		Tools:     []string{ToolSetWidget},
		Model:     "mid",
		MaxChars:  120,
		Fallback:  FreeChatKey,
		Sticky:    true,
	},
	"create_presentation": {
		// Design bygges på lerretet, ikke i chat-tråden: her svarer koden bare
		// med hvordan man åpner det (deckHint i intentwire.go). Selve
		// byggingen skjer med compose/patch, som slås på av et åpent lerret —
		// aldri av en vanlig melding.
		ExplicitOnly:  true,
		Deterministic: true,
		Fallback:      FreeChatKey,
	},

	"export_excel": {
		Knowledge: true,
		// Modellen henter dataene; eksportkortet appendes GARANTERT i kode
		// (aldri «vil du at jeg skal eksportere?»).
		Tools:    []string{ToolQueryDatabase, ToolShowTable},
		Model:    "mid",
		MaxChars: 150,
		Fallback: FreeChatKey,
	},

	"data_question": {
		// Kunnskapslaget er AV på rene tallspørsmål: databasen er kilden, og
		// et notat som havner i konteksten blir servert som om det var et
		// datasvar. Målt: «Hva er marginen per produkt?» hentet 19 notater og
		// svarte «inngangsgebyret er 1 800 kr per produkt» — et notat, ikke
		// et tall fra basen. Sparer også kontekst på den flyten som kjører
		// oftest.
		Knowledge: false,
		// Web-verktøyene er MED: flyter snevrer FOKUS, aldri EVNE. Hybride
		// spørsmål (interne tall × ekstern virkelighet, «korrelasjon mellom
		// strømpris og salgene») var umulige uten.
		Tools: []string{ToolQueryDatabase, ToolShowTable, ToolWebSearch, ToolFetchURL},
		Model: "mid",
		// show_table var en egen flyt til 2026-07-27, med nøyaktig samme
		// verktøy, modell, sticky og fallback — kun MaxChars skilte dem. Og
		// tabellen rendres uansett av tableIntent() i kode, uavhengig av
		// hvilken flyt ruteren valgte. Skillet fantes altså ikke, men ruteren
		// måtte gjette på det og bommet i fire av elleve tilfeller.
		// Svarlengden avgjøres nå av om en tabell FAKTISK ble vist.
		//
		// Websøk er MED (2026-07-28): flyter snevrer FOKUS, aldri EVNE.
		// «Korrelasjon mellom strømpris og salgene» er et datasporsmål som
		// trenger nettet for den ene halvparten — uten web_search svarte
		// modellen sant at den ikke kunne, og sticky holdt den fast selv da
		// brukeren eksplisitt sa «du kan finne dem på nettet». Kvalitet over
		// tokens (Jacobs prioritering); web-verktøyene koster få tokens,
		// i motsetning til db-skjemaet.
		MaxChars: 300, // tette tallsvar
		Fallback: FreeChatKey,
		Sticky:   true, // oppfølgingsspørsmål («sikker?») skal beholde db-verktøyene
	},

	"usage_stats":      {Deterministic: true, ExplicitOnly: true, Fallback: FreeChatKey}, // /forbruk-panelet
	"manage_users":     {Deterministic: true, ExplicitOnly: true, Fallback: FreeChatKey}, // /tilganger-panelet
	"impersonate_user": {Deterministic: true, ExplicitOnly: true, Fallback: FreeChatKey}, // pillen i composeren
	"knowledge_admin":  {Deterministic: true, ExplicitOnly: true, Fallback: FreeChatKey}, // /kunnskap-panelet
	"employees_admin":  {Deterministic: true, ExplicitOnly: true, Fallback: FreeChatKey}, // /ansatte-panelet

	"upload_document": {
		// Klassifisering + lagring har allerede deterministisk løype i chatten.
		ExplicitOnly:  true,
		Deterministic: true,
		Fallback:      FreeChatKey,
	},

	"create_routine": {
		Knowledge: true,
		Tools:     []string{ToolSetupRoutine, ToolListAgents},
		Model:     "mid",
		MaxChars:  200, // kvittering + frekvens
		Fallback:  FreeChatKey,
	},
	"edit_routine": {
		Knowledge: true,
		Tools:     []string{ToolUpdateAgent, ToolListAgents},
		Model:     "mid",
		MaxChars:  200,
		Fallback:  FreeChatKey,
	},

	"m365_files": {
		Knowledge: true,
		Tools:     []string{ToolM365Search, ToolM365Read},
		Model:     "mid",
		MaxChars:  300,
		Fallback:  FreeChatKey,
		Sticky:    true, // fildialog: «åpne den», «hva står det?»
	},

	"email": {
		Knowledge: true,
		// Databaseverktøyene er med så modellen kan bygge Excel-vedlegg
		// («send ordrelisten på e-post») — skjemaet følger verktøyet.
		Tools:    []string{ToolMailSearch, ToolMailRead, ToolMailCompose, ToolQueryDatabase, ToolShowTable},
		Model:    "mid",
		MaxChars: 300,
		Fallback: FreeChatKey,
		Sticky:   true, // e-postdialog: «les den», «hva svarte hun?»
	},

	// Uklar melding: ingen verktøy. Uten søk og database KAN modellen ikke
	// gjette seg til et tema — oppklaringsspørsmålet er eneste utvei. Samme
	// grep som smalltalk: strukturen bærer oppførselen, ikke en instruks.
	UnclearKey: {
		Tools:    nil,
		Model:    "mid", // å se at et referanseledd mangler er dømmekraft
		MaxChars: 120,   // ett kort spørsmål, ikke en utredning
		Fallback: FreeChatKey,
	},

	"advisory": {
		// Motor v6: rådgivning/sparring. Kunnskap PÅ (rådet skal kjenne
		// bedriften), fullt lesebelte (interne tall kan avgjøre rådet),
		// sticky (sparring er flerturs av natur — «hva med X da?» skal
		// beholde rådgiverrollen).
		Knowledge: true,
		Tools:     []string{ToolQueryDatabase, ToolShowTable, ToolWebSearch, ToolFetchURL},
		Model:     "mid",
		MaxChars:  700,
		Fallback:  FreeChatKey,
		Sticky:    true,
	},

	"research_relation": {
		// Motor v6: relasjonsresearch. Fullt lesebelte (profilen kan ligge i
		// interne data ELLER på nettet); Knowledge PÅ fordi subjektet oftest er
		// brukerens egen virksomhet. Sticky: «og i Sverige?» arver klassen.
		// MaxChars 900: svarkontrakten har tre obligatoriske deler
		// (kandidater, negativ avgrensning, skjerpingsspørsmål) — et trangt
		// budsjett amputerer den (målt i probene, MOTOR-V6-PROBER.md).
		Knowledge: true,
		Tools:     []string{ToolWebSearch, ToolFetchURL, ToolQueryDatabase, ToolShowTable},
		Model:     "mid",
		MaxChars:  900,
		Fallback:  FreeChatKey,
		Sticky:    true,
	},
	"recommendation": {
		// Motor v6: anbefaling. Samme belte; ÉN anbefaling med
		// beslutningsregel trenger mindre plass enn relasjonssvaret.
		Knowledge: true,
		Tools:     []string{ToolWebSearch, ToolFetchURL, ToolQueryDatabase, ToolShowTable},
		Model:     "mid",
		MaxChars:  700,
		Fallback:  FreeChatKey,
		Sticky:    true,
	},

	"web_fact": {
		Tools:    []string{ToolWebSearch, ToolFetchURL},
		Model:    "mid",
		MaxChars: 300,
		Fallback: FreeChatKey,
	},
	"teach_fact": {
		// Lære-utsagn («hos oss …», «husk at …»): brukeren FORTELLER noe.
		// Ingen verktøy — et db-kall kan ikke skje her, uansett hva modellen
		// vil; husk-tilbudet (governance v2) fanger selve kunnskapen.
		Tools:    nil,
		Model:    "mid",
		MaxChars: 150, // kort, naturlig kvittering
		Fallback: FreeChatKey,
	},
	"smalltalk": {
		Tools:    nil, // rent samtalesvar — ingen verktøy å misbruke
		Model:    "mid",
		MaxChars: 150,
		Fallback: FreeChatKey,
	},
}

// FlowFor gir flyten for en motoravgjørelse: valgt flyt, ellers fri chat.
func FlowFor(d Decision) (string, Flow) {
	if d.Key != "" {
		if f, ok := Flows[d.Key]; ok {
			return d.Key, f
		}
	}
	return FreeChatKey, Flows[FreeChatKey]
}
