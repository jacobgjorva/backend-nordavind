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
	ToolSetSlide      = "set_slide"
	ToolSetDeck       = "set_deck"
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
		Deterministic: true,
		Fallback:      FreeChatKey,
	},
	"connect_m365": {
		// Deterministisk oppskrift-steg: koden velger riktig steg (koblet/
		// innlogging/app-registrering) — se m365SetupBlock.
		Deterministic: true,
		Fallback:      FreeChatKey,
	},
	"manage_connections": {
		Deterministic: true, // rendrer /tilkoblinger-panelet
		Fallback:      FreeChatKey,
	},

	"create_widget": {
		// "light": verktøydisiplinen var feilfri i e2e-test (riktig SQL med
		// DATE_TRUNC, filter og charttype) — widgeten rendres av koden, og
		// datavernet stopper diktede tall. 10× billigere enn mid.
		Knowledge: true,
		Tools:     []string{ToolSetWidget},
		Model:     "light",
		MaxChars:  120, // widgeten ER svaret; teksten er kun kvittering
		Fallback:  FreeChatKey,
		Sticky:    true, // videre meldinger redigerer samme widget
	},
	"edit_widget": {
		Knowledge: true,
		Tools:     []string{ToolSetWidget},
		Model:     "light",
		MaxChars:  120,
		Fallback:  FreeChatKey,
		Sticky:    true,
	},
	"create_presentation": {
		Knowledge: true,
		// Egne verktøy: presentasjonen bygges slide for slide som patcher, aldri
		// som én stor spec — brukerens egne canvas-endringer skal overleve.
		Tools:    []string{ToolSetSlide, ToolSetDeck},
		Model:    "mid",
		MaxChars: 120, // canvaset er svaret
		Fallback: FreeChatKey,
		Sticky:   true,
	},

	"export_excel": {
		Knowledge: true,
		// Modellen henter dataene; eksportkortet appendes GARANTERT i kode
		// (aldri «vil du at jeg skal eksportere?»).
		Tools:    []string{ToolQueryDatabase, ToolShowTable},
		Model:    "light",
		MaxChars: 150,
		Fallback: FreeChatKey,
	},

	"data_question": {
		// "mid": light degenererte synlig på uklare spørsmål — tegnsøppel og
		// modellens egen verktøy-resonnering lakk ut i svaret («**Verktøyvalg**:
		// … jeg må bruke query_database»), der medium svarte rent. Databasesvar
		// er dessuten det brukeren stoler mest på; feil her koster mer enn
		// modellen sparer.
		Knowledge: true,
		Tools:     []string{ToolQueryDatabase, ToolShowTable},
		Model:     "mid",
		MaxChars:  300, // tette tallsvar
		Fallback:  FreeChatKey,
		Sticky:    true, // oppfølgingsspørsmål («sikker?») skal beholde db-verktøyene
	},
	"show_table": {
		// "mid" av samme grunn som data_question — samme verktøy, samme data.
		Knowledge: true,
		Tools:     []string{ToolQueryDatabase, ToolShowTable},
		Model:     "mid",
		MaxChars:  150, // tabellen er svaret; maks én ledsagende setning
		Fallback:  FreeChatKey,
		Sticky:    true,
	},

	"usage_stats":      {Deterministic: true, Fallback: FreeChatKey}, // /forbruk-panelet
	"manage_users":     {Deterministic: true, Fallback: FreeChatKey}, // /tilganger-panelet
	"impersonate_user": {Deterministic: true, Fallback: FreeChatKey}, // pillen i composeren
	"knowledge_admin":  {Deterministic: true, Fallback: FreeChatKey}, // /kunnskap-panelet
	"employees_admin":  {Deterministic: true, Fallback: FreeChatKey}, // /ansatte-panelet

	"upload_document": {
		// Klassifisering + lagring har allerede deterministisk løype i chatten.
		Deterministic: true,
		Fallback:      FreeChatKey,
	},
	"contract_review": {
		// Kontraktanalysen er en egen deterministisk løype (detect → analyze).
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

	"web_fact": {
		// "light": svaret bygges av søkeresultater og voktes av kilde-
		// kontrollen — småmodellen valgte søk like riktig som medium i test,
		// til en tidel av prisen. Krever LIGHT_TIER=on, ellers mid.
		Tools:    []string{ToolWebSearch, ToolFetchURL},
		Model:    "light",
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
		Model:    "light",
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
