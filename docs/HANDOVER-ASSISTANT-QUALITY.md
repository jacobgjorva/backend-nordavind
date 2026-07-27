# Overlevering: assistentkvalitet (2026-07-27)

Alt under er merget til `main` og deployet til prod (app.nordawind.com), med
mindre annet står. Branch `assistant-quality` peker på samme commit som `main`.

## Åpen sak, start her

**«Moestue & Cask AS har kjøpt for 0 kroner» — men selskapet har over 3 000
rader i `v_Sales`.** Dette er den viktigste ubesvarte feilen.

Bekreftet så langt:

- Prod-tilkoblingen `Operational` (mssql) bruker et **utsnitt**,
  `v_Sales_query` (rad i `connection_views`). Utsnittet filtrerer kun på år,
  ikke på selskap, så dataene er tilgjengelige. Tilgang er altså IKKE årsaken.
- `dbToolContext` fjerner råtabellen fra `allowed` når et utsnitt dekker den
  (`dbtool.go`, `replaced[base] = true`). Bare `v_Sales_query` står igjen.
- Alle gjenopprettingsstrategiene i `dbstrategy.go` starter med
  `tableAllowed(dc, "v_sales")`, som da returnerer usann. `nearestValues`,
  `lookupNoun` og `sqlFixHint` hopper derfor stille ut på nettopp den tabellen
  som har dataene.
- Det forklarer hvorfor bommen ikke ble **reddet**. Det forklarer IKKE hvorfor
  spørringen bommet på et korrekt stavet navn i utgangspunktet.

Neste steg: SQL-logging på vellykkede kall er nå deployet
(`dbtool.go`, `msg=db-spørring` har fått `sql=`). Still spørsmålet i appen og
les av med:

    ssh root@5.75.224.108 'journalctl -u nordavind --since "5 min ago" --no-pager | grep -oE "sql=.{0,300}"'

Mistanker å sjekke mot den faktiske SQL-en: hvilken kolonne den filtrerte på,
og hvordan `&` og suffikset `AS` behandles i sammenligningen.

Deretter: strategiene må slå opp GJENNOM utsnittet i stedet for å avvise
råtabellen, og `dc.columns` / `dc.textColumns` må fylles for utsnitt (i dag
fylles de bare fra `connection_tables`, og bare når tabellen er «detailed» —
den betingede grenen i `dbtool.go` gjør `continue` før indeksen fylles).

## Viktigste fallgruve

**Alt jeg bygde er verifisert mot `Kunder` (Neon, ingen utsnitt). Prod bruker
`Operational` med utsnitt.** Det er grunnen til at feilen over overlevde en
hel dag med grønne tester. Test mot en tilkobling med utsnitt før noe her
regnes som ferdig.

To andre fallgruver som kostet tid i dag:

- Testmiljøet kjørte uten `INTENT_ENGINE=on` mens prod har den på. To hele
  målerunder var ugyldige. Start alltid lokal server med `INTENT_ENGINE=on`.
- En gammel serverprosess holdt port 8099, så en testrunde traff forrige
  binær og «viste ingen forbedring». Kjør `lsof -ti:8099 | xargs kill -9`
  først, og sjekk at loggen ikke sier `bind: address already in use`.

## Hva som er bygget

**Utholdenhet (`internal/api/dbstrategy.go`).** Databasekall har typede utfall
(`dbOK` / `dbEmpty` / `dbNoAccess` / `dbFailed`) i stedet for prosa. Ved bom
prøver koden selv neste vei: nærmeste navn ved fritekstbom, faktiske kolonner
ved SQL-feil, den ærlige redegjørelsen (`explainAttempts`) når alt er prøvd.
Et aggregat uten treff klassifiseres som `dbEmpty`, ikke `dbOK` — det er én rad
med 0, og feilklassifiseringen var det som lot «har kjøpt for 0 kroner» slippe
ut som et funn.

**Innsiktslaget (`internal/api/insight.go`).** Sju observasjoner regnet ut av
rader vi allerede har i minnet, levert som én setning etter svaret: skjev
fordeling, ubrukt kolonne, uferdig måned, radgrense, gamle data, duplikater,
hull. Null modellkall. Fem porter mot støy i `deliverInsight`. `MaxChars` fra
flyt-tabellen fungerer nå som plassbudsjett — før i dag endte den i en
loggsetning og gjorde ingenting.

**Arbeidsnarrasjon (`internal/api/narrate.go`).** Deterministisk fremdriftstekst
i kode, null tokens. `n.seed(brukertekst)` gjør at variantrotasjonen starter
ulikt per melding; uten den fikk første steg alltid variant 0, og brukeren så
samme åpningslinje hver gang.

**Ruting.** `show_table`-flyten er slått sammen med `data_question` — skillet
var kun `MaxChars`, og tabellen rendres uansett av `tableIntent()` i kode.
`manage_users` er skjerpet til å eie «hvem har tilgang»-spørsmål.
Intent-eval: **92,4 %** (121/131), porten er grønn.

**Migrasjonsbug rettet.** `est_rows` ble lagt til inne i `DeleteConnection`, så
kolonnen fantes bare hvis noen hadde slettet en tilkobling. Manglet den,
droppet hele databasen stille ut av verktøyet og assistenten lette i OneDrive
etter kundedata. Nå i oppstartsmigreringen.

## Prinsipp som har holdt

Tre ganger i dag bygde jeg en port som utløste på **hvordan svaret var
formulert** (regex på «finnes ikke»), og alle tre måtte skrotes — modellen
finner stadig nye vendinger. Det som fungerer er å utløse på **fakta**: ble
navnet søkt på i noen spørring, ga aggregatet null, ble en tabell vist.
Formuleringssjekker er teknisk gjeld fra første linje.

## Ikke gjort

- Prompt-endring 2 (`dbRule`: «kommentarer om datagrunnlaget legger koden til
  selv») ble godkjent, implementert og **reversert**. Målt som netto
  forverring: den fjernet modellens merknad om den ubrukte kredittkolonnen i
  kjøringene der innsiktslaget ikke traff. Hører hjemme igjen når innsiktslaget
  dekker de tilfellene pålitelig.
- `answerStyle` står fortsatt av, med vilje. Den ber modellen vurdere om den
  «har noe ekte», noe den ikke kan — den ser JSON, ikke aritmetikk.
- Branchen `token-budget` har tre commits som konflikter med `main` (brain- og
  design-arbeidet har flyttet seg forbi). Egen sak.

## Testverktøy

`convo.py` og scenariofilene ligger i sesjonens scratchpad, ikke i repoet:
`/private/tmp/claude-501/.../a308cd0b-.../scratchpad/`. `hard.json` har åtte
flerturs-scenarier som avdekket det meste av dette. Verdt å flytte inn i repoet
ved siden av `cmd/flow-sim` — den tester bare enkeltmeldinger, og alle feilene
i dag satt mellom turene.
