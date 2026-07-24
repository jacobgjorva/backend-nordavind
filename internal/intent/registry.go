// Package intent er rutingsmotoren: en melding matches mot et register av
// flyt-nøkler via embeddings (ren matte), med et lite enum-begrenset
// dommer-kall kun når matten er usikker. Alt etter valget eies av kode.
//
// Designregler (avtalt 2026-07-24):
//   - Ingen prompt-tweaks og ingen enkelttilfelle-regex — nye behov er nye
//     rader i registeret, aldri ny logikk.
//   - Fail-open: enhver feil (timeout, upstream nede, tom melding) gir
//     IntentNone → helt vanlig fri chat. Motoren kan aldri blokkere brukeren.
//   - Hver oppløsning logges med fasit-mulighet — det er treningssettet for en
//     fremtidig finetunet klassifiserer (samme arkitektur, bedre innmat).
package intent

// Intent er én skuff i registeret: en flyt appen kan håndtere deterministisk.
type Intent struct {
	// Key er stabil identifikator — flyt-laget slår opp på denne.
	Key string
	// Description er én setning om hva flyten gjør (embeddes).
	Description string
	// Examples er virkelige måter brukere ber om flyten på (embeddes hver
	// for seg; score = beste treff blant beskrivelse + eksempler). 3-8 stk,
	// inkluder korte/skjeve varianter — de er det embeddings bommer på.
	Examples []string
	// AdminOnly begrenser flyten til admin-rollen; for andre er den usynlig.
	AdminOnly bool
}

// Registry er alle flytene motoren kan rute til. Rekkefølgen er likegyldig.
// VEDLIKEHOLD: hver reelle feilruting i produksjon skal bli (1) en ny linje i
// testdata/eval.jsonl og (2) om nødvendig et nytt Example her. Aldri ny logikk.
var Registry = []Intent{
	{
		Key:         "connect_database",
		Description: "Koble til en ny database (Postgres, MySQL, SQL Server) med host, port og passord",
		Examples: []string{
			"koble til Postgres-basen vår",
			"vi har en ny MySQL-server som må inn",
			"legg til en databasetilkobling",
			"postgres",
			"koble på kundedatabasen",
			"opprett en ny kobling",
		},
		AdminOnly: true,
	},
	{
		Key:         "connect_m365",
		Description: "Koble til Microsoft 365, Outlook, OneDrive eller SharePoint via innlogging",
		Examples: []string{
			"koble til Microsoft 365",
			"m365",
			"vi må få inn OneDrive",
			"koble til outlook-kontoen",
			"microsoft",
		},
		AdminOnly: true,
	},
	{
		Key:         "manage_connections",
		Description: "Se, deaktivere eller slette eksisterende datakilder og tilkoblinger",
		Examples: []string{
			"slett Neon-koblingen",
			"deaktiver kunde-databasen",
			"hvilke tilkoblinger har vi?",
			"fjern databasetilkoblingen",
			"vis datakildene våre",
		},
		AdminOnly: true,
	},
	{
		Key:         "create_widget",
		Description: "Lage en ny widget, graf eller visualisering av bedriftsdata",
		Examples: []string{
			"lag et linjediagram over månedlig omsetning",
			"trenger en graf over ordre per status",
			"ny widget med topp 10 kunder",
			"lag en donut over fordelingen",
			"visualiser salget per region",
		},
	},
	{
		Key:         "edit_widget",
		Description: "Endre en eksisterende widget: farger, filtre, spørring eller visningstype",
		Examples: []string{
			"gjør stolpene grønne i widgeten",
			"legg til kunde-filter på grafen",
			"bytt grafen fra linje til stolper",
			"endre widgeten til å vise hele året",
			"fjern filteret på widgeten",
		},
	},
	{
		Key:         "create_presentation",
		Description: "Lage en presentasjon eller slides med live tall",
		Examples: []string{
			"lag en presentasjon om salget i år",
			"bygg slides til styremøtet med ferske tall",
			"ny presentasjon for kvartalsgjennomgangen",
			"presentasjon",
			"lag et deck om kundeveksten",
		},
	},
	{
		Key:         "export_excel",
		Description: "Eksportere en tabell eller spørring til Excel eller OneDrive med live data",
		Examples: []string{
			"eksporter tabellen til Excel",
			"legg denne i OneDrive med live kobling",
			"last ned som xlsx",
			"få dette inn i et regneark",
			"excel-eksport av ordrelisten",
		},
	},
	{
		Key:         "data_question",
		Description: "Spørsmål om bedriftens egne tall: ordre, kunder, omsetning, salg, lager",
		Examples: []string{
			"hvor mye omsatte vi for i juni?",
			"hvem er største kunden vår?",
			"hvor mange ordre fikk vi i går?",
			"hva er snittordren i år?",
			"hvilke produkter selger best?",
			"hvordan ligger vi an mot budsjettet?",
			"når på dagen selger vi mest?",
		},
	},
	{
		Key:         "show_table",
		Description: "Be om en tabell eller liste over rader fra bedriftens database",
		Examples: []string{
			"trenger en tabell med de 10 nyligste ordrene",
			"list opp alle ordre fra ACKES",
			"vis kundene som tabell",
			"tabell over leveranser denne uken",
			"gi meg radene for juli",
		},
	},
	{
		Key:         "usage_stats",
		Description: "Se plattformens eget AI-forbruk: tokens brukt, hva AI-en koster oss, kvoter per bruker",
		Examples: []string{
			"hvor mye tokens har vi brukt denne måneden?",
			"hva koster AI-bruken oss?",
			"vis forbruket per bruker",
			"hvor mye har vi igjen av kvoten?",
			"token-forbruk i dag",
		},
		AdminOnly: true,
	},
	{
		Key:         "manage_users",
		Description: "Administrere plattformbrukere: invitere nye, fjerne, endre roller og tilganger",
		Examples: []string{
			"inviter kari@family.no",
			"gjør Jacob til admin",
			"fjern brukeren til Ola",
			"legg til en ny bruker",
			"hvem har tilgang til plattformen?",
		},
		AdminOnly: true,
	},
	{
		Key:         "impersonate_user",
		Description: "Simulere eller opptre som en annen bruker for å se deres opplevelse og tilganger",
		Examples: []string{
			"simuler Kari sin bruker",
			"jeg vil se appen slik en vanlig ansatt ser den",
			"bytt til Ola sitt perspektiv",
			"opptre som en medlem-bruker",
			"test appen som en annen bruker",
		},
		AdminOnly: true,
	},
	{
		Key:         "knowledge_admin",
		Description: "Se, redigere eller godkjenne bedriftskunnskapen AI-en husker",
		Examples: []string{
			"hva husker AI-en om oss?",
			"godkjenn kunnskapsforslagene",
			"slett faktumet om leveringstid",
			"vis kunnskapsgrafen",
			"hvilke fakta ligger inne?",
		},
		AdminOnly: true,
	},
	{
		Key:         "upload_document",
		Description: "Laste opp eller lagre et dokument som varig bedriftskunnskap",
		Examples: []string{
			"lagre dette dokumentet som bedriftskunnskap",
			"husk innholdet i denne PDF-en",
			"legg rutinebeskrivelsen inn i kunnskapsbasen",
			"lær deg dette dokumentet",
			"arkiver denne guiden",
		},
	},
	{
		Key:         "contract_review",
		Description: "Analysere en kontrakt eller juridisk dokument og markere kritiske klausuler",
		Examples: []string{
			"gå gjennom denne leieavtalen for meg",
			"hva bør jeg passe på i denne kontrakten?",
			"analyser denne avtalen før jeg signerer",
			"finn fellene i denne NDA-en",
			"er det noe farlig i dette dokumentet?",
		},
	},
	{
		Key:         "create_routine",
		Description: "Sette opp en gjentakende agent-rutine som kjører automatisk på fast intervall",
		Examples: []string{
			"trenger verdi av bitcoin hvert 15 min",
			"sjekk lagerstatus hver morgen kl 07",
			"overvåk konkurrentens priser daglig",
			"lag en agent som følger med på ordreinngangen",
			"varsle meg hver uke om nye kunder",
			"si fra når aksjen går over 15 kr",
			"følg med på kursen og varsle meg ved endring",
		},
	},
	{
		Key:         "edit_routine",
		Description: "Endre en eksisterende agent-rutine: frekvens, klokkeslett, varsling eller oppgave",
		Examples: []string{
			"kjør rutinen hver halvtime i stedet",
			"endre agenten til å varsle på e-post",
			"sett rutinen på pause",
			"bytt tidspunktet til kl 06",
			"stopp bitcoin-agenten",
		},
	},
	{
		Key:         "employees_admin",
		Description: "Se eller redigere bedriftens ansattregister med roller og avdelinger",
		Examples: []string{
			"legg til en ny ansatt i registeret",
			"hvem jobber i salgsavdelingen?",
			"oppdater tittelen til Kari",
			"vis ansattlisten",
			"fjern en sluttet ansatt",
		},
		AdminOnly: true,
	},
	{
		Key:         "web_fact",
		Description: "Faktaspørsmål om verden utenfor bedriften: nyheter, priser, kurser, personer, hendelser",
		Examples: []string{
			"hva er kursen på polight-aksjen?",
			"når ble Telenor grunnlagt?",
			"hva er bitcoin verdt nå?",
			"hvem vant valget?",
			"hva blir været i Oslo i morgen?",
			"hva er en embedding?",
			"forklar hva inflasjon er",
		},
	},
	{
		Key:         "smalltalk",
		Description: "Hilsener, takk, småprat eller spørsmål om hva assistenten kan gjøre",
		Examples: []string{
			"takk for hjelpen!",
			"hei, hva kan du gjøre?",
			"god morgen",
			"hvem er du?",
			"det var alt for nå",
		},
	},
}

// byKey gir oppslag fra nøkkel til intent (bygges én gang).
var byKey = func() map[string]Intent {
	m := make(map[string]Intent, len(Registry))
	for _, in := range Registry {
		m[in.Key] = in
	}
	return m
}()

// Lookup henter en intent på nøkkel.
func Lookup(key string) (Intent, bool) {
	in, ok := byKey[key]
	return in, ok
}
