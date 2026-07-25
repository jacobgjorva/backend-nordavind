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
	ToolContactPerson = "contact_person"
	ToolSetWidget     = "set_widget"
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
// ved sammensatte ønsker (MethodMulti) og som Fallback for alle flyter.
const FreeChatKey = "free_chat"

// Flows er flyt-tabellen. VEDLIKEHOLD: nye behov = ny rad (og ny intent i
// registeret) — aldri spesialtilfelle-logikk utenfor tabellen.
var Flows = map[string]Flow{
	FreeChatKey: {
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
		Tools:    []string{ToolSetWidget},
		Model:    "mid",
		MaxChars: 120, // widgeten ER svaret; teksten er kun kvittering
		Fallback: FreeChatKey,
		Sticky:   true, // videre meldinger redigerer samme widget
	},
	"edit_widget": {
		Tools:    []string{ToolSetWidget},
		Model:    "mid",
		MaxChars: 120,
		Fallback: FreeChatKey,
		Sticky:   true,
	},
	"create_presentation": {
		Tools:    []string{ToolSetWidget},
		Model:    "mid",
		MaxChars: 120, // canvaset er svaret
		Fallback: FreeChatKey,
		Sticky:   true,
	},

	"export_excel": {
		// Eksport-modalen eies av UI-et; modellen skal bare peke på tabellen.
		Deterministic: true,
		Fallback:      FreeChatKey,
	},

	"data_question": {
		Tools:    []string{ToolQueryDatabase, ToolShowTable},
		Model:    "mid",
		MaxChars: 300, // tette tallsvar
		Fallback: FreeChatKey,
		Sticky:   true, // oppfølgingsspørsmål («sikker?») skal beholde db-verktøyene
	},
	"show_table": {
		Tools:    []string{ToolQueryDatabase, ToolShowTable},
		Model:    "mid",
		MaxChars: 150, // tabellen er svaret; maks én ledsagende setning
		Fallback: FreeChatKey,
		Sticky:   true,
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
		Tools:    []string{ToolSetupRoutine, ToolListAgents},
		Model:    "mid",
		MaxChars: 200, // kvittering + frekvens
		Fallback: FreeChatKey,
	},
	"edit_routine": {
		Tools:    []string{ToolUpdateAgent, ToolListAgents},
		Model:    "mid",
		MaxChars: 200,
		Fallback: FreeChatKey,
	},

	"m365_files": {
		Tools:    []string{ToolM365Search, ToolM365Read},
		Model:    "mid",
		MaxChars: 300,
		Fallback: FreeChatKey,
		Sticky:   true, // fildialog: «åpne den», «hva står det?»
	},

	"web_fact": {
		Tools:    []string{ToolWebSearch, ToolFetchURL},
		Model:    "mid",
		MaxChars: 300,
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

// AskLabels er korte menneskelige merkelapper per flyt — brukes KUN i
// oppklaringsspørsmålet når dommeren er reelt i tvil («vil du X eller Y?»).
var AskLabels = map[string]string{
	"connect_database":    "koble til en database",
	"connect_m365":        "sette opp Microsoft 365",
	"manage_connections":  "administrere tilkoblingene",
	"create_widget":       "lage en graf",
	"edit_widget":         "endre en widget",
	"create_presentation": "lage en presentasjon",
	"export_excel":        "eksportere til Excel",
	"data_question":       "få svar fra bedriftens egne tall",
	"show_table":          "se en tabell med rader",
	"usage_stats":         "se AI-forbruket",
	"manage_users":        "administrere brukere",
	"impersonate_user":    "se appen som en annen bruker",
	"knowledge_admin":     "se på bedriftskunnskapen",
	"upload_document":     "lagre et dokument som kunnskap",
	"contract_review":     "få en kontrakt gjennomgått",
	"create_routine":      "sette opp en fast rutine",
	"edit_routine":        "endre en rutine",
	"employees_admin":     "jobbe med ansattregisteret",
	"m365_files":          "finne noe i filene dine",
	"web_fact":            "få et faktasvar fra nettet",
	"smalltalk":           "bare prate",
	FreeChatKey:           "ha et råd eller en vurdering",
}
