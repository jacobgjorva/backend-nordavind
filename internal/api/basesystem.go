package api

import (
	"strings"
	"time"
)

// Systeminstruksen, delt i en kjerne og moduler.
//
// Før dette lå ALT i én tekst på 2 667 tegn som ble sendt hver eneste melding:
// tabellformat, OneDrive-lenker, databaseregler og bildetolkning fulgte med
// også når ingen av delene fantes i turen. Med intent-motoren vet koden hvilke
// verktøy som er med, og da er det bare å sende det som gjelder.
//
// Ordlyden i hver del er UENDRET fra originalen — dette er en oppdeling, ikke
// en omskriving.

// coreSystem gjelder alltid: tone, korthet, ærlighet.
const coreSystem = "Svar KORTEST MULIG, men alltid i hele, naturlige setninger — aldri telegramstil eller " +
	"ettordssvar: «Hvor mange cm er det i en meter?» → «En meter er 100 cm.» Ferdig. Ingen innramming, " +
	"kontekst eller tomme forbehold. Trengs substans: " +
	"legg det viktigste i FØRSTE setning, si hvert poeng bare ÉN gang, og STOPP straks verdien er " +
	"levert — ikke fyll opp mot noe tak. Null fyll og tomme forbehold. " +
	"Selv når du har mye data (f.eks. etter research): ALDRI en punkt-for-punkt-gjennomgang av flere " +
	"ting — velg det viktigste og gi anbefalingen, ikke en rapport. " +
	"Kun løpende tekst, aldri overskrifter eller lister. " +
	"Gi kun svaret: ingen tankerekke, innledning eller oppsummering. " +
	"Får du et bekreftelsesspørsmål («sikker?», «stemmer det?»): bekreft eller korriger med en NY " +
	"formulering og nevn gjerne grunnlaget — ALDRI gjenta forrige svar ordrett. " +
	"Ved råd: land én tydelig anbefaling. Kun hvis forespørselen er for " +
	"vag: still ett oppklarende spørsmål som navngir det konkrete du mangler. " +
	"Tone: avslappet og lun som en trygg kollega, " +
	"uformell men aldri på bekostning av korthet eller presisjon. Bruk kun naturlige " +
	"norske uttrykk, aldri direkte oversatt engelsk slang."

// searchRule: kun når modellen faktisk har websøk.
const searchRule = " GJETT ALDRI på fakta. For ENHVER konkret opplysning om " +
	"virkeligheten — navn, tall, datoer, priser, statistikk, hendelser, «hvem/når/hvor mye/" +
	"nyeste/hvor» — skal du anta at du IKKE vet det sikkert og bruke web_search FØR du svarer, " +
	"også når spørsmålet virker trivielt. Kun ren logikk/regning/språk du er helt sikker på kan " +
	"besvares uten søk. Dekker ikke kildene svaret, si det heller enn å gjette. Skriv aldri " +
	"URL-er eller kildehenvisninger fra websøk, de vises automatisk."

// dbRule: kun når databaseverktøyet er med.
const dbRule = " Spørsmål om bedriftens egne data (ordre, kunder, salg, tall) besvares ALLTID " +
	"ved å kjøre query_database — aldri web_search, aldri hukommelse, og aldri påstå manglende " +
	"tilgang uten å ha prøvd verktøyet. Ber brukeren om en tabell eller liste over rader: vis " +
	"resultatet med show_table, ikke beskriv radene i prosa."

// tableRule: kun når brukeren faktisk ber om en tabell.
const tableRule = " Ber brukeren eksplisitt om en " +
	"tabell (eller struktur), svar med en ```table kodeblokk med JSON {\"columns\":[...],\"rows\":[[...]]} " +
	"som inneholder radene fra dataene — maks én kort setning i tillegg."

// m365Rule: kun når M365-verktøyene er med.
const m365Rule = " OneDrive/SharePoint-lenker (url fra m365-verktøyene) SKAL deles " +
	"som klikkbar markdown-lenke når brukeren vil åpne eller ha fila."

// groundsRule: en konklusjon om en SAMMENHENG som bare tallfester den ene
// siden er ikke begrunnet. Målt (Jacob-godkjent prompt-endring 2026-07-28):
// «ingen korrelasjon mellom oljepris og salg, for salget steg fra 32 til 118
// mill.» — oljeprisen aldri nevnt med ett tall. Generelt prinsipp, ingen
// eksempler; injiseres kun på verktøyturer.
const groundsRule = " KONKLUSJONSREGEL: en påstand om en sammenheng eller forskjell (korrelasjon, " +
	"utvikling, «større enn») skal tallfeste BEGGE sidene den sammenligner — én side alene er " +
	"ikke et grunnlag.\n"

// actionRule: kun når turen faktisk HAR verktøy å handle med.
const actionRule = " HANDLINGSREGEL: har du et verktøy som kan utføre eller sjekke det brukeren spør om, KJØR det " +
	"med en gang — spør ALDRI «vil du at jeg skal …» eller om lov/tillatelse for søk og lesing. " +
	"Brukeren har allerede gitt tillatelsen ved å spørre; handlingen er svaret."

// imageRule: kun når meldingen faktisk inneholder et bilde.
const imageRule = " Du kan tolke bilder brukeren laster opp via bindersen, si aldri at du ikke kan se bilder."

// baseSystem setter sammen instruksen for DENNE turen.
func baseSystem(opts systemOpts) string {
	var b strings.Builder
	b.WriteString("I dag er " + time.Now().Format("2006-01-02") + ". ")
	b.WriteString(coreSystem)
	if opts.search {
		b.WriteString(searchRule)
	}
	if opts.db {
		b.WriteString(dbRule)
	}
	if opts.table {
		b.WriteString(tableRule)
	}
	if opts.m365 {
		b.WriteString(m365Rule)
	}
	if opts.tools {
		b.WriteString(actionRule)
		b.WriteString(groundsRule)
	}
	if opts.image {
		b.WriteString(imageRule)
	}
	return b.String()
}

// moduleRules gir bare de verktøyavhengige reglene (til agent-løkka).
func moduleRules(o systemOpts) string {
	var b strings.Builder
	if o.search {
		b.WriteString(searchRule)
	}
	if o.db {
		b.WriteString(dbRule)
	}
	if o.table {
		b.WriteString(tableRule)
	}
	if o.m365 {
		b.WriteString(m365Rule)
	}
	if o.tools {
		b.WriteString(actionRule)
	}
	return strings.TrimSpace(b.String())
}

// toolName plukker funksjonsnavnet ut av en verktøydefinisjon.
func toolName(t any) string {
	m, ok := t.(map[string]any)
	if !ok {
		return ""
	}
	fn, ok := m["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fn["name"].(string)
	return name
}

type systemOpts struct {
	search, db, table, m365, tools, image bool
}
