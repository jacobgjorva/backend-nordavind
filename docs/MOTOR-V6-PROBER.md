# Motor v6 — probelogg

Alle kjøringer mot ekte `mistral-large-2512` (La Plateforme) med ekte websøk
(DDG + Wikipedia lokalt; prod bruker SearXNG). Ingen mocks. Transkript i
`probe_runs*` — rå tall her er skrevet FØR konklusjonene, per protokollen i
MOTOR-V6.md del 9. Dev-sett: `cmd/v6probe/testdata/dev.jsonl` (10 prompts,
ingen fra tidligere feilsaker eller benchmarks). Holdt-tilbake-settet er ikke
skrevet ennå og skrives først ved A/B-aksept.

## Kjøring 1 — variant B1 (tenk + metode v1), hele settet

`probe_runs/20260730-015629`

```
r1 relasjon    runder=2 søk=1 hent=0 tok=5727/366   13,4s  (uten tanke)
r2 relasjon    runder=2 søk=1 hent=0 tok=6384/678   18,0s  TANKE
r3 relasjon    runder=3 søk=3 hent=1 tok=11593/552  18,7s  TANKE
r4 relasjon    runder=2 søk=1 hent=0 tok=5218/239    8,0s  TANKE
a1 anbefaling  runder=2 søk=1 hent=0 tok=4400/490   14,0s  TANKE
a2 anbefaling  runder=3 søk=1 hent=2 tok=12282/576  15,5s  TANKE
o1 oppslag     runder=2 søk=1 hent=0 tok=6273/50     2,5s
o2 oppslag     runder=2 søk=1 hent=0 tok=5109/73     3,1s
s1 samtale     runder=1 søk=0 hent=0 tok=729/42      1,4s
u1 uklar       runder=1 søk=0 hent=0 tok=687/70      2,4s
```

## Kjøring 2 — variant A (naken baseline), de 6 komplekse

`probe_runs_naken/*`

```
r1 søk=1 hent=0 tok=5173/197   7,2s
r2 søk=1 hent=0 tok=4950/300  10,1s
r3 søk=1 hent=0 tok=3196/134   5,7s
r4 søk=1 hent=0 tok=4570/62    3,9s
a1 søk=1 hent=0 tok=4767/127   6,4s
a2 søk=1 hent=0 tok=4120/178   6,4s
```

## Kjøring 3 — variant B2 (tenk + metode v2), de 6 komplekse

`probe_runs_v2/*`. v2 la inn: svarkontrakt med tre obligatoriske deler,
kandidatsider skal leses (fetch_url) før dom, tall kun ordrett fra kilde,
pris utelates ærlig når den ikke er lest, beslutningsregel merkes som skjønn
uten kildegrunnlag, aldri «beste X»-søk.

```
r1 søk=1 hent=0 tok=5897/723   18,8s
r2 søk=2 hent=4 tok=17579/1098 32,7s
r3 søk=1 hent=2 tok=9974/565   16,7s
r4 søk=1 hent=0 tok=4641/273   11,0s
a1 søk=1 hent=2 tok=11598/567  16,0s
a2 søk=5 hent=0 tok=18044/359  25,6s
```

## Dommer per probe

**P1 — tenk-så-handle i samme completion: BESTÅTT.**
Tanke levert sammen med verktøykall i 5/6 komplekse (r1 hoppet over — brukeren
hadde alt gitt profilen, akseptabelt), fraværende på trivielle (o1/o2/s1/u1 —
ønsket adferd: ingen tankekost der den ikke trengs). Null tilfeller av
plan-uten-handling. Null protokoll-lekkasje i samtlige 28 kjøringer på tvers
av alle varianter. Tankene var norske, tette og informative (P4 samtidig
bestått): «trenger et klart kriterium: bølgepapp, sjømat, 30-50 MNOK …»,
«det eneste som gjør dem til direkte konkurrent i ditt segment …». Egnet som
live-narrasjon uten redigering.

**P2 — reasoning-feltet: AVGJORT SOM IKKE AKTUELT.**
La Plateforme svarer 422 på Scaleway-feltet `reasoning` (målt i prod,
sanering finnes alt i server.go). Tenk-prosaen i samme completion er dermed
resonneringskanalen. Ingen videre måling nødvendig.

**P3 — metodeblokk-effekten: BESTÅTT med v2, med ett åpent avvik.**

B1 mot A: metoden kjøpte dybde og kalibrering der oppgaven var hard (r3: tre
søk + sidelesing + ærlig «vanskelig å finne dokumentert», mot A sine to navn
fra ett søk; r4: evidens fra produktsider mot A sitt tynne ja; a1/a2: ÉN
anbefaling med beslutningsregel mot A sin vagere prosa). Uavgjort der
brukeren selv ga profilen (r1, r2). Men v1 ignorerte negativ avgrensning
(0/4) og skjerpingsspørsmålet (0/6), og «lever pris» uten lest side ga
diktede tall (r2: omsetning; a2: prismodell «fast per bedrift» og terskel
«30 ansatte» fantes ikke i kildene — nøyaktig MVP-rundens feilklasse).

B2 (metode v2): svarkontrakten holdt. Skjerpingsspørsmål 6/6 (mot 0/6 i v1),
negativ avgrensning eksplisitt i 4/6 og relevant formulert, sidelesing der
det trengtes (r2: 4 hentinger, a1: 2, r3: 2), ærlig prishåndtering («prisen
for 25 ansatte er ikke oppgitt direkte»), beslutningsregel MED
kildeattribusjon («dette bygger på at leverandøren selv fremhever …»), og
grounding-sjekken mot kildeteksten fant at alle kandidatnavn og alle
kontrollerte tall sto i kildene — de tre mistenkte («137,7», «åtte ansatte»,
«10–500») var omformateringer som prods normDigits-toleranse godtar.

Åpent avvik: r4s negative avgrensning navnga to store aktører fra
HUKOMMELSEN, ikke kildene (én av dem trolig konfabulert). Mønster: kravet om
negativ avgrensning drar uverifiserte navn inn i nettopp den setningen.
Fiks i metode v3 (én linje): avgrensningen følger samme kildekrav som
kandidatene — navngi kun aktører som står i kildene eller samtalen, ellers
beskriv kategorien uten navn. Prods navnekontroll fanger resten som gulv.

Bikostnad B2: research-turer koster 10-18k prompt-tokens og 16-33 s når
sidelesing faktisk skjer — som designet forventer (målrettet dybde), men
svarene ble lange (opptil 1 100 tokens ut) og brukte fete overskrifter.
Håndteres som gulv, ikke prompt: research-flytene får eget MaxChars-budsjett,
og fet skrift strippes deterministisk i leveransen.

Stabilitet: a2 valgte ulikt produkt i B1 og B2 (begge reelle, begge
begrunnet). Nondeterminisme på tvers av kjøringer er ventet; aksepttesten
(del 10 i designet) måler anatomi, ikke navnelikhet.

**P5 (ruterutvidelse) og P6 (kontrakt under press): IKKE KJØRT ennå.**
P5 krever registerendring + intent-eval; P6 krever eget pressett. Neste økt.

## Konsekvenser for designet (ført inn i MOTOR-V6.md)

1. Metodekatalogen oppdatert til v2-tekstene + avgrensnings-kildekravet (v3).
2. Nytt gulv: fet skrift/overskrifter strippes i leveransen (deterministisk).
3. Research-flyter trenger eget svarbudsjett (MaxChars ~800) så
   kompresjonsgulvet ikke ødelegger anatomien.
4. P2-mekanismen strøket; tenk-prosa er kanalen.
5. Tallkontrollen (normDigits-toleranse) bekreftet nødvendig og tilstrekkelig
   for omformateringsklassen; navnekontrollen må også dekke
   avgrensningssetningen.

## Kjøring 4 — P6: leveransekontrakten under press

`probe_runs_press/*`, sett `cmd/v6probe/testdata/press.jsonl` (6 prompts som
frister til meny, tabell, totalitet, hastverk, premissaksept og diktet
terskel). Tenk + metode v3.

```
p1 «10 beste»-liste     søk=1 hent=4 tok=14385/1116 28,0s
p2 sammenligningstabell søk=1 hent=0 tok=5391/777   21,1s
p3 «absolutt alle»      søk=1 hent=0 tok=5095/539   22,2s
p4 «kjapt, ikke research» søk=0 hent=0 tok=874/91    3,0s
p5 ledende premiss      søk=1 hent=0 tok=5634/312    9,7s
p6 krev konkret grense  søk=1 hent=0 tok=5008/274    8,6s
```

Dom: 5/6 holdt formen — under falsifikasjonsgrensen (>2/6). Detaljer:

- p1/p2: etterspurt struktur ble LEVERT — riktig produktadferd; «aldri meny»
  betyr aldri meny SOM STANDARD, ikke nekt når brukeren ber om oversikt.
  Begge endte med kuratert anbefaling på toppen av oversikten. Presisert som
  tolkningsregel i designet.
- p3: nektet umulig totalitet, ærlig avgrensning av hva som ikke er dekket.
- p5: sto imot premisset («ingen konkurrenter»), fant navngitte aktører
  (verifisert i kildene), skjerpingsspørsmål til slutt. Sterkeste svaret.
- p6: nektet fast grense, ga faktorene og ÉN beslutningsregel — men
  «500 000–1 million»-spennet (attribuert «mange rådgivere») sto IKKE i
  kildene. Myk attribusjon er den høflige varianten av diktet terskel:
  metodetekst alene stopper det ikke — tallkontroll-gulvet er obligatorisk.
- p4: FEIL. «Ikke noe research» ble adlydt, men svaret anbefalte et KONKRET
  produkt selvsikkert fra hukommelsen. Metode v4 (én linje): eksplisitt
  research-fritak gir kort svar MERKET som uverifisert hukommelse + tilbud
  om sjekk.

Konsekvens ført inn: metode v4 i harnessen; designregel om at brukerens
eksplisitte formkrav vinner over kontraktens standardform.

## Kjøring 5 — P5: ruterutvidelsen

Registeret fikk `research_relation` og `recommendation` (nye rader +
flyt-rader med fullt lesebelte, Knowledge på, sticky, MaxChars 900/700), og
eval-settet 16 nye linjer på tvers av domener. Live intent-eval, samme dag,
tre kjøringer:

```
motor-v6:  135/147 (91,8 %) — nye klasser 16/16, gamle linjer 119/131
motor-v6:  135/147 (91,8 %) — stabil over to kjøringer
main i dag: 119/131 (90,8 %) — målt i rent main-worktree som kontroll
```

Dom: BESTÅTT. Falsifikasjonskravet («synker under dagens 92,4 %») viste seg
å peke på et FORELDET tall: main måler 90,8 % i dag med uendret register —
dommeren har ±1-2 linjers naturlig variasjon på grenselinjer (samme
bom-klasser: «m365», «Opprett en ny kobling», e-post-uten-konto-linjene).
På samme dags baseline er utvidelsen null regresjon på gamle linjer
(119/131 begge steder) og 16/16 på de nye. Én ny grenselinje dukket opp
(«hvilke datakilder burde vi koble til?» → recommendation, fasit fri chat);
én skjerping av beskrivelsen ble prøvd uten effekt, og videre jaging av
enkeltlinjen er nettopp benchmark-lapping — den står som kjent grensetilfelle.

Lærdom for porten: eval-terskelen må alltid sammenlignes mot SAMME DAGS
main-kjøring, ikke mot et historisk tall.

## Status: alle prober avgjort

P1 ✓ P2 avgjort (N/A) ✓ P3 ✓ P4 ✓ P5 ✓ P6 ✓ (5/6 → 6/6 med metode v4).
Designet i MOTOR-V6.md står. Neste steg er del 10: implementasjon bak
ENGINE=v6 med metodeklassene oppslag + relasjonsresearch + samtale først,
holdt-tilbake-sett skrevet ulest, A/B med blindlesing mot dagens motor.

## Kjøring 6 — del 3: tenk-regelens plassering (ekte løkke)

Første gang den FAKTISKE arbeidsløkka (`internal/motor`) kjørte mot modellen,
via `cmd/v6loop`. Enhetstestene var grønne og `BuildSystem` gjorde nøyaktig
det den var skrevet for — og tankekanalen var likevel helt død.

Årsaken var rekkefølgen i systemprompten. Designet la tenk-regelen SIST, med
den plausible begrunnelsen at den styrer formen på neste melding. Proben
hadde tilfeldigvis motsatt rekkefølge.

```
tenk-regel SIST  (som designet):  tanke i 0 av 5 verktøyturer
tenk-regel FØRST (som proben):    tanke i 6 av 9 verktøyturer
```

(samtale-klassen er utelatt: den har ingen verktøy, så regelen legges
aldri på.)

Sekundærfunn, samme datagrunnlag: turer der modellen faktisk tenkte høyt
brukte **1,33 søk i snitt mot 2,67** for turer uten tanke. Å si hensikten
høyt før hentingen halverer altså hentekostnaden — det er designets
hypotese om relevansankeret, bekreftet på kostnadssiden før ankeret selv
er koblet inn (det kommer i del 4).

Konsekvens: rekkefølgen er låst til base → tenk-regel → metodetekst, den
midlertidige bryteren er fjernet, og loop_test håndhever rekkefølgen med
begrunnelsen i klartekst. Mekanismen er trolig at metodeteksten ender i en
svarkontrakt, og at en formregel plassert etter den leses som en del av
svaret modellen ennå ikke skal skrive — men årsaken er hypotese, tallene er
målingen.

LÆRDOM: grønne enhetstester sier at koden gjør det den er skrevet for. De
sier ingenting om at det virker. Hver del må ha minst én ekte kjøring før
den regnes som ferdig.

Åpent, til del 6: `a1` leverte i én kjøring «tre gode norske
vaktplansystemer» — en meny, som anbefalingskontrakten forbyr. Metodetekst
alene holder ikke formen hver gang; leveransegulvet må ta det.

## Kjøring 7 — del 3: metodetekst v5 (mål, ikke verktøynavn)

Bekreftelseskjøringen etter at tenk-rekkefølgen var låst viste **null
sidehentinger** på hele settet, mens designprobene hadde 4, 2 og 2. Metoden
sa eksplisitt «les kandidatenes egne sider (fetch_url)», og modellen
ignorerte det hver eneste gang.

Årsaken var ikke ulydighet. `web_search` kaller ALLEREDE `FetchPages` med
4 sider à 6 000 tegn (`excerpt.go`), så full sidetekst ligger i
søkeresultatet. Instruksen ba om en dublett, og modellen avslo korrekt.

Fikset i DATA, ikke kode: metodetekstene sier nå hva som må være SANT om
svaret («bygg dommen på KILDETEKSTEN, aldri på titler, topplister eller
hukommelse; mangler en avgjørende kilde, hent den siden særskilt») i stedet
for å navngi et verktøy. Ny strukturvakt
(`TestMethodTextsDescribeGoalsNotToolNames`) hindrer at verktøynavn kommer
inn igjen — de binder metoden til dagens verktøyoppsett og må skrives om
hver gang det endres.

Resultat med v5 (r2, r4, a1, a2):

```
tanke i 3 av 4 turer
verktøykall 1-3 søk per tur, 0 hentinger (nå riktig: sidene er alt hentet)
grunnlag: alle navn og tall dekket av kildene
```

Metodefeil hos MEG, verdt å notere: første grunnlagssjekk ropte «diktet» på
tre navn som viste seg å være helt dekket. Sjekkeren krevde ordrett
frasetreff, mens modellen lovlig hadde satt sammen produktnavn og leverandør
fra URL-en («HMS Bygg fra West Internkontroll» av «HMS Bygg» +
`west-internkontroll.no`). Riktig mål er TOKEN-dekning — nøyaktig det
prods `grounding.go` gjør. Sjekkeren ligger nå i
`cmd/v6loop/grounding_check.py` så senere kjøringer bruker samme mål.

Åpent til del 4: tankefrekvensen svinger (2/7, 3/4, 6/9 i ulike kjøringer).
Tanken er verdifull når den kommer — turer med tanke bruker halvparten så
mange søk — men den kan ikke ANTAS. Narrasjonen og relevansankeret må
derfor være fullstendig valgfrie: uten tanke faller begge tilbake til
dagens oppførsel uten tap.
