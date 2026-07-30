# Motor v6 — byggeplan

Åtte deler. Hver del landes separat med grønne tester og `ENGINE=v6` av =
byte-identisk prod-adferd. Ingen del regnes ferdig uten sitt eget
ferdig-kriterium.

## Bærende byggeregel: justering uten lapping

All adferd som kan trenge justering bor i DATA, aldri i kodegrener:

- Ny oppgaveklasse → ny rad i metodekatalogen + ny rad i intent-registeret.
  Aldri en if-setning.
- Endret dybde/kost → tall i metoderadens budsjett (søk, hentinger, runder,
  svarlengde). Aldri en spesialkvote i løkka.
- Endret svarform → metoderadens tekst. Aldri en formulerings-regex.
- Gulvene utløses KUN på observerbare fakta (radantall, kvotestand, tall
  finnes i kildene, tabell vist) — aldri på hvordan modellen ordla seg.
- Én mekanisme per problemklasse. Oppstår en ny feilklasse, får den ett
  generelt svar i katalog eller gulv — enkelttilfeller lappes aldri
  (bevist livsfarlig i v1-v5).

Mock-avklaring: modellsvar mockes ALDRI som kvalitetsbevis (Jacobs regel,
målt i MVP-runden). Deterministisk kode (budsjettteller, stripping,
aggregering) enhetstestes med syntetiske input som normalt — det er
kontrollflyt, ikke kvalitet.

## Del 1 — pakke, kontrakter og bryter

Ny pakke `internal/motor`, adskilt fra api-monolitten (agent.go er 1 961
linjer; v6 skal ikke bli linje 1 962). Pakken definerer kontraktene og eier
ingen nettverkskode:

- `Turn`-tilstand (hentet evidens, kilder, tanker, kvoteforbruk, db-utfall).
- Grensesnitt: `ModelCaller` (én completion, streaming), `ToolRunner`
  (utfør ett verktøykall → typet resultat), `Emitter` (SSE ut),
  `Narrator`-adapter. api-pakken implementerer dem med EKSISTERENDE kode —
  ingen duplisering av søk/db/SSE.
- `ENGINE=v6`-porten + overleveringsreglene: utfører-flyter (widget,
  eksport, rutiner, e-post, design, paneler) går urørt til legacy; ukjent
  verktøykall fra modellen = hel overlevering (som dagens handoff).

Ferdig: pakken kompilerer med fakes for kontraktene, full suite grønn,
flagg av → ingen endret payload (test som sammenligner bytes).

## Del 2 — metodekatalogen som data

`internal/motor/methods.go`: én struct per klasse — nøkkel, probet tekst
(v4 fra harnessen, ordrett), budsjett {søk, hentinger, runder, MaxChars},
og flyt→metode-mappingen (research_relation→relasjon, recommendation→
anbefaling, web_fact→oppslag, data_question→analyse, smalltalk→samtale,
free_chat→ingen). Ingen metode = naken løkke (fail-open).

Vaktene er strukturelle, ikke semantiske: enhetstest håndhever
lengdetak, at ingen tekst inneholder et EKSEMPEL (bransje/entitet/case-ord),
at rutingen peker på klasser som finnes, at budsjettene henger sammen
(fetch uten search er umulig), at ingen rad sprenger kostnadstaket, at
standardbudsjettet fortsatt speiler legacys tak, og at klasser uten
rutingsvei er dokumentert i stedet for glemt. Katalogendring er en
dataendring med diff på én rad.

Harness-drift løses STRUKTURELT, ikke med en diff-test: cmd/v6probe
importerer katalogen fra internal/motor. Kopien i proben er borte, så
probene tester per definisjon det produksjon kjører.

STATUS: FERDIG. Katalogtestene grønne. Én reell konflikt fanget av
testene — relasjonsklassen trenger 4 hentinger (probe r2 brukte nøyaktig
4, og dybden var det som gjorde svaret godt), mens legacys tak er 3. Riktig
invariant er derfor et hardt kostnadstak (MaxBudget), ikke «aldri over
standarden»; standarden er låst til legacys tall så en tur UTEN metode
oppfører seg nøyaktig som i dag.

## Del 3 — arbeidsløkka

Den native løkka fra proben, portert til pakken: bygg systemprompt (dagens
kjerne + modulregler + metodeblokk + tenk-regelen), kjør runder til
modellen slutter å kalle verktøy eller metoderadens rundetak er nådd.
Kvotene håndheves i `ToolRunner` per metoderad (samme mekanisme som dagens
context-kvoter — verktøyet svarer ærlig «kvoten er brukt», modellen stoles
aldri på for telling). Tool-resultater tilbake som tool-meldinger, uendret
kontrakt.

Ferdig: løkke-enhetstester (rundetak, kvotestopp, handoff, tom-respons)
med fake ToolRunner; én EKTE ende-til-ende-kjøring per metodeklasse mot
modellen (v6probe-settet) uten protokoll-lekkasje.

## Del 4 — tankekanalen

Prosaen som kommer sammen med verktøykall: (1) streames som arbeidssteg
gjennom narratoren (eksisterende junk-/ekko-filtre; merket steg, aldri
svar), (2) brukes som relevansanker — utdragsrangeringen får spørsmål +
turens siste tanke som vektor i stedet for bare siste brukermelding.
Endringen i api er én parameter inn i runWebSearchFor-adapteren.

Ferdig: enhetstest på filtrering og anker-sammensetning; manuell lesing av
narrasjonen fra én ekte kjøring (P4-kriteriene: norsk, tett, aldri
instruks-lekkasje).

## Del 5 — gulvene

Porteres som DELT kode, ikke kopier: aggregering >20 rader, typede
db-utfall + ærlig-tomt-tekst, nærmeste-navn-redning, tabellgaranti,
innsiktslag, neste-steg-lag, kildenote, historikk-kapp, samtaleutdrag,
søke-cache. Nytt: fet-/overskrift-stripping i leveransen (deterministisk),
og navnekontroll som dekker hele svaret inkl. avgrensningssetningen.
Der funksjonene i dag er private i api, flyttes de til motor-pakken og
re-eksponeres — flytting, aldri omskriving (de er målt).

STATUS: FERDIG. Gulvene er KOBLET, ikke kopiert: `internal/motor/floor.go`
eier kontrakten og rekkefølgen (tabell → observasjon → kildenote → neste
steg → slutt), `internal/api/motorfloors.go` oppfyller den ved å kalle
insight.go, dbstrategy.go og narrate.go der de står. Ingen målt kode er
flyttet eller skrevet om.

Nytt i motor-pakken: `StripEmphasis` (fet skrift og overskrifter ut av
prosa, kodeblokker urørt) og `HonestEmpty` (skiller «prøvde ingenting» fra
«fant ingenting», og lar databasens egen redegjørelse vinne).

Svarbudsjettet avledes av metoden når den har eget tak (relasjon 900,
anbefaling 700), ellers arves flytens — så research-svarets tre deler ikke
amputeres av et budsjett satt for tallsvar.

ÅPENT OG MERKET: neste-steg-laget er IKKE portert. Det finnes bare på
engine-experiment, og å kopiere umålt kode hit ville brutt regelen om at
gulv skal være verifiserte. Gulvet står som en tom, navngitt metode i
stedet for å late som det er dekket.

## Del 6 — leveransen

Modellens sluttekst er utkastet. ETT omskrivingskall kun når koden har
regnet fakta modellen ikke så (stats/Pearson) eller utkastet er tomt —
aldri kaskade. Tallkontroll med normDigits-toleranse som telemetri; harde
avvik → maks ÉN omskriving (eksisterende tolerante løype). MaxChars fra
flyt-raden (900/700 for research-klassene — kompresjonsgulvet skal aldri
amputere svarkontraktens tre deler). Deretter gulv-appends og [DONE].

Ferdig: leveranse-enhetstester (omskriving kun ved fakta/tomt, én-gangs
reground, strip før emit); tallkontrollen fanger de probede
diktings-tilfellene (r2-omsetning, p6-spennet) i replay av transkriptene —
ekte modellsvar som testdata, ikke mocks.

## Del 7 — ruting og klebrighet

Koble flyt→metode i intentwire (registeret og flyt-radene ligger alt på
branchen, P5-målt). Klebrighet: elliptisk oppfølging arver forrige metode
via eksisterende sticky-mekanisme — ingen ny tilstand. Lav
ruter-sikkerhet → ingen metode → naken løkke.

Ferdig: intent-eval grønn MOT SAMME DAGS main-baseline (P5-lærdommen:
aldri sammenlign med historiske tall); sticky-testene utvidet med
metodearv.

## Del 8 — A/B-aksept og utrulling

- Holdt-tilbake-sett: 20 prompts i nye domener, skrives til slutt og leses
  først ved kjøring (anti-overtilpasning, samme regel som plan-eval).
- Kjør begge motorer lokalt (ENGINE=v6 på/av) mot settet; transkript
  arkiveres; blindlesing side om side mot anatomi-kriteriene fra designet
  del 10 (alle må holde: relasjonsanatomi ≥8/10, oppslag ±10 % kall/tokens,
  null lekkasje, null udekkede tall, evaler grønne).
- Jacob leser blindsammenligningen og eier prod-beslutningen. Deploy med
  kill-switch; legacy-løypa står urørt som fallback.

Ferdig: akseptrapport i docs/ med rå tall før konklusjon, som probeloggen.

## Rekkefølge og avhengigheter

1→2→3 er kjernen og bygges sekvensielt. 4 og 5 er parallelle etter 3.
6 krever 3+5. 7 krever 2. 8 til slutt. Hver del er én commit-serie på
motor-v6; ingenting merges til main før del 8 er lest og godkjent.
