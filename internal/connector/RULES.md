# Connector-regler

Regler for implementering av nye connectors (datakilder og integrasjoner).
Dagens to familier — database (postgres/mysql/mssql i `internal/connector/`)
og OAuth/SaaS (M365 i `internal/api/m365.go`) — er starten; alle nye skal
følge dette. UTKAST til gjennomgang med Jacob.

## 1. Sikkerhet (ufravikelig)

- Hemmeligheter lagres KUN kryptert (samme crypto som `Creds` i dag);
  access-tokens lagres aldri, kun refresh-tokens/credentials.
- Datakilder er strengt read-only fra chat: SafeQuery-mønsteret er normen —
  én setning, keyword-vern, tabell-allowlist, radgrense, OG et backstopp på
  protokollnivå (read-only transaksjon / scopes med minst mulig tilgang).
  Regex-vern alene er aldri nok.
- Alle kall har eksplisitt timeout og rimelige connection-caps; en treg
  kilde skal aldri kunne henge chatten.
- Tilgang styres per tabell/ressurs med user_ids som i dag; ny connector
  uten tilgangsstyring merges ikke. Oppsett/sletting er AdminOnly.
- Hemmeligheter skrives ALDRI i chatmeldinger: når en connector trenger
  passord/nøkler, spawnes credential-panelet inline i chatten (samme mønster
  som admin-panelene). Panelet poster direkte til sitt eget endepunkt,
  verdien går aldri inn i meldingshistorikk, logger eller LLM-kontekst,
  feltet er maskert og tømmes etter innsending. Limer brukeren likevel et
  passord i chat, avvises det med henvisning til panelet.

## 2. Arkitektur

- Én pakke per connector-familie; delt kontrakt: Open/verify (ping),
  Introspect (skjema/ressurser), SafeQuery/hent (lesing). Nye kilder
  implementerer samme grensesnitt — aldri spesialtilfeller i chat-koden.
- Konfig samles av connector-agenten (som M365 app-creds i dag): DB først,
  env som fallback. Ingen hardkodede tenant-detaljer.
- Feil er fail-open mot brukeren: tydelig norsk feilmelding i chat, aldri
  stacktrace, aldri blokkert samtale.
- OAuth-flyter: state-vern lagret i DB (overlever restart), single-tenant
  endepunkter der leverandøren krever det, «lukk fanen»-avslutning.

## 3. Intent og flyt (porten til chatten)

- Ny connector = ny rad i intent-registeret (beskrivelse + min. 5 eksempler,
  AdminOnly ved behov) + rad i flyt-tabellen med verktøy/modell/grenser.
  Aldri logikk utenfor tabellene.
- Før merge: eget sim-sett etter web_fact-malen (positive + feller på tvers
  av nabo-connectors), gjennomgått av Jacob, kjørt grønt; bommene inn i
  eval.jsonl. Hoved-eval skal fortsatt være grønn.

## 3b. Oppskrifter (onboarding-guide per connector)

Hver connector registrerer en deklarativ oppskrift som agenten leser og
guider fra — aldri fri prosa-improvisasjon, aldri ny agentlogikk per kilde.
En oppskrift består av nummererte steg, og hvert steg er én av tre typer:

- `info`: hva brukeren skal gjøre/hente og nøyaktig hvor (à la Azure-guiden:
  «portal.azure.com → App registrations → …»), med venting på bekreftelse.
- `panel`: hvilket inline-panel som spawnes (credential-skjema, nøkkelfelt,
  OAuth-vindu) og hvilke felter agenten kan forhåndsfylle. Alt sensitivt
  skjer HER — aldri i chat.
- `verify`: hvilket verktøy som bekrefter steget (à la check_m365).
  Agenten påstår aldri suksess uten et grønt verify-steg.

Oppskriften eier rekkefølge og formuleringssteder; agenten eier bare tonen
og feilhjelpen. Ny connector = ny oppskrift + evt. nytt panel + verify-
verktøy. Format (Go-struktur eller JSON i koden) besluttes ved første
connector etter M365; M365-guiden migreres til formatet samtidig.

## 4. Kvalitet og drift

- Introspeksjon skal tåle store skjemaer: paginering/kostnadsindikator i UI,
  aldri hele skjemaet ukritisk inn i LLM-kontekst.
- Radgrenser: 100 for LLM-kontekst, eksplisitt N for widget/eksport
  (SafeQueryN-mønsteret).
- Hver connector får en helsesjekk (brukes av manage_connections-panelet)
  og logger nok til å feilsøke uten tilgang til kundens data.
- Nye drivere/SDK-er: minst mulig avhengigheter, ren Go der mulig
  (kryss-kompilering til prod er en del av deploy-løypa).

## 5. Prosess

- Regelendringer her avtales med Jacob før de tas i bruk.
- En connector er «ferdig» først når: kryptert lagring verifisert, read-only
  bevist mot ekte kilde, intent-sim grønn, tilgangsstyring testet med
  member-bruker, og helsesjekk synlig i panelet.
