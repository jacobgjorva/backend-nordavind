package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Connector-agenten: ren agent, ingen skriptet flyt. Brukeren vil koble til en
// ekstern kilde; agenten hjelper, og støtter applikasjonen kilden, fikser den
// det via verktøyene. Ærlig om det som ikke støttes ennå.

// connectorAgentSystem bygges per server (redirect-URLen må inn i Azure-guiden).
func (s *Server) connectorAgentSystem() string {
	return "Du hjelper brukeren (admin) å koble applikasjonen til en ekstern kilde. Norsk, kort og " +
		"vennlig, ett spørsmål av gangen.\n" +
		"STØTTET NÅ:\n" +
		"- Databaser: PostgreSQL, MySQL og SQL Server. PASSORD SKRIVES ALDRI I CHATTEN: du spør aldri etter " +
		"passord, og du kaller aldri connect_database selv. I stedet svarer du med en credential-blokk som " +
		"åpner et sikkert skjema hos brukeren — forhåndsfyll feltene du kjenner (aldri passord):\n" +
		"```credential\n{\"name\":\"Kundedata\",\"driver\":\"postgres\",\"host\":\"db.example.com\",\"port\":5432,\"database\":\"kunder\",\"user\":\"leser\"}\n```\n" +
		"Limer brukeren inn en connection string, parser du den til feltene i blokken — men limer de inn et " +
		"passord, ber du dem bruke skjemaet og gjengir det aldri. Skjemaet tester og lagrer tilkoblingen selv " +
		"og melder fra til brukeren; be dem si fra når det er gjort.\n" +
		"- Microsoft 365 (OneDrive/SharePoint, live Excel-eksport): kall connect_m365. Får du beskjed om at " +
		"app-registrering mangler, GUIDER du brukeren gjennom den i chatten, ett steg av gangen, og venter på " +
		"bekreftelse mellom stegene:\n" +
		"  1) Gå til portal.azure.com → App registrations → New registration. Navn: «Nordavind».\n" +
		"  2) Under Redirect URI: velg Web og lim inn " + s.msRedirectURL() + "\n" +
		"  3) API permissions → Add → Microsoft Graph → Delegated: User.Read, Files.ReadWrite, offline_access.\n" +
		"  4) Certificates & secrets → New client secret → kopier verdien med en gang.\n" +
		"  Be så om Application (client) ID, Directory (tenant) ID (samme oversiktsside) og secret-verdien, kall " +
		"save_m365_app med alle tre, og kall deretter " +
		"connect_m365 — innloggingen åpnes automatisk hos brukeren.\n" +
		"ALT ANNET (CSV, Excel-filer, Databricks, API-er osv.): si ærlig at det ikke er støttet ennå, og nevn " +
		"gjerne hva som ER støttet. Ikke lov noe, ikke simuler.\n" +
		"Ikke gjengi passord, secrets eller nøkler i svarene dine. Etter et vellykket verktøykall: bekreft kort " +
		"hva som ble koblet til.\n" +
		"Du lager ALDRI lenker eller URL-er selv — innloggingsvinduet åpnes automatisk av connect_m365; vil " +
		"brukeren prøve igjen, kall connect_m365 på nytt. ABSOLUTT REGEL: du påstår ALDRI at noe er tilkoblet uten at et verktøy (connect_database/check_m365) " +
		"har bekreftet det i denne samtalen. Sier brukeren «gjort» etter en Microsoft-innlogging: kall " +
		"check_m365 FØR du svarer, og gjengi kun det verktøyet sier."
}

// connectorAgentTools er verktøyene connector-agenten kan bruke.
func connectorAgentTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "connect_database",
				"description": "Opprett en databasetilkobling. Tester tilkoblingen først; feiler den, får du " +
					"feilmeldingen tilbake så du kan hjelpe brukeren rette.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":     map[string]any{"type": "string", "description": "kort visningsnavn, f.eks. «Kundedata»"},
						"driver":   map[string]any{"type": "string", "description": "postgres | mysql | mssql"},
						"host":     map[string]any{"type": "string"},
						"port":     map[string]any{"type": "integer"},
						"database": map[string]any{"type": "string"},
						"user":     map[string]any{"type": "string"},
						"password": map[string]any{"type": "string"},
					},
					"required": []string{"name", "driver", "host", "database", "user", "password"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "connect_m365",
				"description": "Start Microsoft 365-tilkobling (OAuth). Innloggingsvinduet åpnes automatisk " +
					"hos brukeren — be dem fullføre der. Svarer verktøyet at app-registrering mangler, guid " +
					"brukeren gjennom Azure-registreringen og bruk save_m365_app først.",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "check_m365",
				"description": "Sjekk om Microsoft 365 FAKTISK er koblet til. KALL ALLTID denne før du " +
					"uttaler deg om tilkoblingsstatus — aldri påstå suksess uten.",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "save_m365_app",
				"description": "Lagre Azure app-registreringen (fra guiden) for organisasjonen. Kalles én gang " +
					"med Application (client) ID og client secret-verdien brukeren limer inn.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"client_id":     map[string]any{"type": "string", "description": "Application (client) ID fra Azure"},
						"client_secret": map[string]any{"type": "string", "description": "client secret-VERDIEN (ikke id-en)"},
						"directory_id":  map[string]any{"type": "string", "description": "Directory (tenant) ID fra samme Azure-side — påkrevd for single-tenant-apper"},
					},
					"required": []string{"client_id", "client_secret", "directory_id"},
				},
			},
		},
	}
}

// runCheckM365 gir agenten den FAKTISKE tilkoblingsstatusen — aldri gjetting.
func (s *Server) runCheckM365(ctx context.Context) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	if acc, err := s.store.M365Account(user.ID); err == nil {
		return "TILKOBLET som " + acc.Email + "."
	}
	// Allerede koblet: si det i stedet for å starte ny OAuth.
	if email, ok := s.m365Connected(ctx); ok {
		return "Microsoft 365 er allerede koblet til som " + email + "."
	}
	if _, _, _, ok := s.msAppCreds(user.TenantID); !ok {
		return "IKKE tilkoblet, og app-registrering mangler. Guid brukeren gjennom Azure-registreringen først."
	}
	return "IKKE tilkoblet. Innloggingen er ikke fullført — be brukeren prøve på nytt (connect_m365), og be dem si fra om vinduet viste en feilmelding."
}

// runSaveM365App lagrer tenantens Azure-app (secret krypteres).
func (s *Server) runSaveM365App(ctx context.Context, rawArgs string) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	var a struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		DirectoryID  string `json:"directory_id"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil ||
		strings.TrimSpace(a.ClientID) == "" || strings.TrimSpace(a.ClientSecret) == "" {
		return "Mangler client_id eller client_secret."
	}
	enc, err := connector.Encrypt(s.credsKey, []byte(strings.TrimSpace(a.ClientSecret)))
	if err != nil {
		return "Intern feil ved kryptering."
	}
	if err := s.store.SetM365App(user.TenantID, strings.TrimSpace(a.ClientID), strings.TrimSpace(a.DirectoryID), enc); err != nil {
		return "Kunne ikke lagre app-registreringen."
	}
	return "App-registreringen er lagret. Kall connect_m365 nå for å starte innloggingen."
}

// connectDatabaseArgs er feltene connect_database tar imot.
type connectDatabaseArgs struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// runConnectDatabase tester og oppretter tilkoblingen (samme sti som HTTP-API-et).
func (s *Server) runConnectDatabase(ctx context.Context, rawArgs string, emit func(string)) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	var a connectDatabaseArgs
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return "Ugyldige felter."
	}
	if a.Port == 0 {
		switch a.Driver {
		case "postgres":
			a.Port = 5432
		case "mysql":
			a.Port = 3306
		case "mssql":
			a.Port = 1433
		}
	}
	creds := connector.Creds{
		Driver: a.Driver, Host: a.Host, Port: a.Port,
		Database: a.Database, User: a.User, Password: a.Password,
	}
	db, err := connector.Open(ctx, creds)
	if err != nil {
		return "Tilkoblingen feilet: " + err.Error()
	}
	db.Close()

	plain, _ := json.Marshal(creds)
	enc, err := connector.Encrypt(s.credsKey, plain)
	if err != nil {
		return "Intern feil ved kryptering."
	}
	conn, err := s.store.CreateConnection(user.TenantID, strings.TrimSpace(a.Name), a.Driver, enc)
	if err != nil {
		return "Kunne ikke lagre tilkoblingen."
	}
	s.log.Info("tilkobling opprettet via connector-agent", "tenant", user.TenantID, "driver", a.Driver)
	// Frontend laster tilkoblingslisten på nytt.
	meta, _ := json.Marshal(map[string]any{"nordavind_connection_created": conn.ID})
	emit("data: " + string(meta))
	return "Tilkoblingen «" + a.Name + "» er opprettet og testet OK (id=" + conn.ID + "). " +
		"Neste steg for brukeren er å velge hvilke tabeller AI-en skal ha tilgang til."
}

// runConnectM365 starter OAuth-flyten og ber frontend åpne vinduet.
func (s *Server) runConnectM365(ctx context.Context, emit func(string)) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok {
		return "Ikke innlogget."
	}
	// Allerede koblet: si det i stedet for å starte ny OAuth.
	if email, ok := s.m365Connected(ctx); ok {
		return "Microsoft 365 er allerede koblet til som " + email + "."
	}
	if _, _, _, ok := s.msAppCreds(user.TenantID); !ok {
		return "App-registrering mangler for organisasjonen. Guid brukeren gjennom Azure-registreringen " +
			"(stegene i instruksene dine) og lagre med save_m365_app først."
	}
	url, err := s.msAuthURL(user)
	if err != nil {
		return "Kunne ikke starte Microsoft-innloggingen."
	}
	meta, _ := json.Marshal(map[string]any{"nordavind_m365_auth": url})
	emit("data: " + string(meta))
	return "Innloggingsvinduet er åpnet hos brukeren. Be dem logge inn og godkjenne der. VIKTIG: du vet " +
		"IKKE om innloggingen lykkes — når brukeren sier de er ferdige, kall check_m365 og rapporter KUN " +
		"det den svarer. Påstå ALDRI at tilkoblingen er fullført uten bekreftelse fra check_m365."
}
