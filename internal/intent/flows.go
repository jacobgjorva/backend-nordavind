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
		Tools:    []string{ToolConnectM365, ToolCheckM365, ToolSaveM365App},
		Model:    "mid",
		MaxChars: 400,
		Fallback: FreeChatKey,
		Sticky:   true, // Azure-oppsett er en dialog
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
