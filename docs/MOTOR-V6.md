# Motor v6 — metodemotoren

Design før kode. Skrevet 2026-07-30, revidert samme dag etter proberunde
1-3 (se MOTOR-V6-PROBER.md). P1, P2, P3 og P4 er avgjort mot ekte modell;
P5 og P6 gjenstår før implementasjonseksperimentet i del 10.

## 1. Målbildet

Visjonen er en assistent som forstår hva brukeren egentlig prøver å oppnå,
skaper fremgang mot det, og sier fra om det brukeren ikke visste at den burde
spørre om. Ikke en svarrobot.

Det målbildet har en målbar anatomi. Et svar i verdensklasse på et
klassifiseringsspørsmål («hvem er våre direkte konkurrenter») gjør fire ting:

1. **Profilerer subjektet først.** Hvem ER aktøren det spørres om — segment,
   størrelse, kanal, særpreg — lest fra aktørens egne kilder, ikke antatt.
2. **Definerer relasjonen.** «Direkte konkurrent» betyr ingenting før
   kriteriet er satt: konkurrent på HVA — portefølje, kanal, kunde, pris?
   Kriteriet følger av beslutningen brukeren skal ta, ikke av ordbok.
3. **Avgrenser negativt.** Navngir de åpenbare store aktørene og sier hvorfor
   de IKKE kvalifiserer. Halve verdien av et presist svar er det som
   ekskluderes — det er dét som beskytter brukeren mot feilbeslutning.
4. **Skjerper med ETT spørsmål** — kun når svaret faktisk avhenger av et
   profilhull, og spørsmålet navngir hullet konkret.

Samme anatomi gjelder generelt, ikke bare research: et datasvar skal si hva
tallene viser, hva de ikke viser, og hva som ville endret konklusjonen. En
anbefaling skal gi ETT valg med den ene beslutningsregelen som ville snudd
det. Til og med småprat skal forstå hva brukeren er ute etter.

Feilen vi bygger oss bort fra er den motsatte anatomien, målt i prod
2026-07-30: bred kategorisøk → prominente navn → «betydelig overlapp»
begrunnet med at alle selger i samme brede kanaler → selvsikker veiledning
på falsk klassifisering. Ser riktig ut, er negativ verdi.

## 2. Bevisgrunnlaget

Alt under er målt i dette prosjektet, ikke ment. Designet står på dette.

**Målt å virke:**

- Én lineær native tool-løkke er robust: 0 motorfeil, 0 protokoll-lekkasje,
  1,19 kall i snitt (MVP runde 1, 16 ekte oppgaver).
- Modellen er en god skrivemotor når koden gir ferdigregnet faktagrunnlag
  (compute-first, motor v2/v3: 25x færre tokens, 18/18).
- Kode-eide gulv virker: aggregering før modellen ser >20 rader, ærlig
  «ingen rader»-tekst i stedet for null-rad, typede db-utfall, innsikts- og
  neste-steg-lag uten tokens, tallkontroll med toleranse som telemetri.
- «Utfør, aldri annonser»-instruksen fjernet planlegging-i-prosa-feilen
  fullstendig (screening 2026-07-29).
- Profil-først-direktiv + skarp svarform løftet målt relasjonssvar fra vagt
  bransjenivå til nisjenivå (2026-07-28) — men var regex-trigget og ble
  derfor korrekt forkastet som mekanisme. Innsikten består: metoden virker,
  utløseren var feil.
- Semantisk intent-ruting med eval som port (92,4 %), parallell med
  kunnskapsoppslag under tidsbudsjett, fail-open. Sanksjonert semantikk-vei.
- Full sidehenting fremfor snutter (feilkoblinger med snippets, målt).
- Kapp historikk (12 meldinger), kompakt samtaleutdrag, utdragsrangering
  med embeddings.

**Målt å feile:**

- Ekstra funksjonskall-protokoller (mission, investigate, goalstate,
  adaptive) destabiliserer Large 3: etter et strukturert plankall begynner
  den å skrive verktøykall som rå tekst — 11/16 og 10/16 ødelagte svar.
- Challenge/dommer-ledd: 8/16 protokollfeil, 43,6 s snitt, og samme modell
  forsvarer sitt eget utkast.
- Naken native løkke uten metode: søker for sjelden, svarer overkonfident,
  bygger på svake kilder, dikter terskler («churn > 40 %»), anbefaler
  nedlagte produkter.
- Regex/nøkkelord-ruting: fanger ikke omskrivinger, overtilpasses eksempler.
- Prompt-stabling: å legge alle regler i hver prompt gjorde svarene MÅLBART
  verre. Regler i kode; prompt kun for det irreducibelt semantiske.
- Formuleringsvakter (regex på «finnes ikke»): modellen finner alltid nye
  vendinger. Utløs på fakta, aldri på ordlyd.
- Separat planleggerkall per tur: kostnad på hver tur, dobbel planlegger,
  ny forankringsfeil (Codex-syklus 2, avvist med rette).
- Rigid state gjentatt per runde: tokens, formatdrift, selvmotsigelser.

**Kjernediagnosen** (verifisert i kode, Opus-syklus 1): motoren binder
hentebudsjettet FØR subjekt, kriterium og evidensbehov er etablert, og tidlig
generisk evidens selvforsterker — runde 1-søk betinges av runde 0-kildene.
Kvotene begrenser kostnad, ikke omfang. Ingen mekanisme leser spørsmålet mot
det som ble hentet.

## 3. Prinsipper

1. **Modellen eier mening, koden eier fakta.** Semantikk (hva spørres det
   egentlig om, hva ville en skeptiker angrepet, hvordan formulere) løses av
   modellen. Alt observerbart (kvoter, aggregering, tallkontroll, budsjett,
   rekkefølgegarantier, blokk-appends) løses deterministisk i kode.
2. **Metode, ikke protokoll.** Kvalitet kommer fra å gi modellen riktig
   FREMGANGSMÅTE i naturlig språk før den handler — aldri fra å tvinge den
   gjennom skjemaer. Large 3 følger instruks godt og protokoll dårlig.
3. **Tenk før verktøy, i samme kall.** Resonnering skjer som prosa i samme
   completion som verktøykallene — null ekstra kall, og tanken gjenbrukes
   (del 5.2). Aldri et separat planleggerkall.
4. **Én metode per tur, valgt semantisk.** Intent-motoren (eksisterende,
   eval-portet) velger metodeklasse. Bommer den, degraderer turen til naken
   løkke — aldri verre enn i dag. Ingen nøkkelord, ingen regex.
5. **Negativ avgrensning er leveranse.** Ved enhver klassifisering skal
   svaret si hva som IKKE kvalifiserer og hvorfor. Dette står i metoden,
   ikke i kjerneprompten.
6. **Usikkerhet snevrer påstanden, stopper aldri turen.** Nedgrader form
   (månedlig for daglig, omtrentlig for eksakt), si hva som ble brukt.
7. **Ærlighet er billigere enn vakter.** Tallkontroll er telemetri pluss
   maks ÉN omskriving på observert avvik — aldri kaskade, aldri dommer.
8. **Hver mekanisme bevises isolert mot ekte modell før den komponeres.**
   Ingen mocks. Falsifikasjonskriterier settes FØR målingen.

## 4. Arkitekturen

Én tur gjennom v6:

```
melding
  │
  ├─ intent-ruting (eksisterende, parallell m/kunnskap, fail-open)
  │    → flyt (verktøysett) + METODEKLASSE (ny dimensjon) + budsjetter
  │
  ├─ systemprompt: kjernestemme + verktøyregler (modulært, som i dag)
  │                + ÉN metodeblokk (300-600 tegn, kun matchende klasse)
  │
  ├─ ARBEID (native løkke, maks runder per metode)
  │    modellen streamer: prosa + verktøykall i samme completion
  │      ├─ prosaen → «tanke»-narrasjon til bruker (filtrert, aldri svar)
  │      │            og → relevansanker for utdragsrangering
  │      ├─ verktøykall → kode utfører: validering, kvoter per metode,
  │      │   cache, aggregering >20 rader, typede db-utfall, ærlig tomt
  │      └─ resultater tilbake som tool-meldinger
  │
  ├─ LEVERANSE
  │    modellens sluttekst = utkast
  │    kode har regnet stats/korrelasjon? eller utkast tomt?
  │      → ETT skrivekall med kompakt kontekst (persona+utdrag+fakta)
  │    tallkontroll (tolerant) → logg; harde avvik → maks én omskriving
  │
  └─ GULV (kode, null tokens)
       tabellgaranti · innsikt · neste steg · kildenote · [DONE]
```

Utfører-flytene (widget, eksport, rutiner, e-post, design) beholder sine
egne kontrakter og går utenom v6 — som i dag. v6 eier chat, data, research,
anbefaling, samtale.

Kill-switch: `ENGINE=v6` (av → dagens vei, byte-identisk).

## 5. Detaljene som gjør den skarp

### 5.1 Metodeblokken

Én blokk per tur, valgt av ruteren, injisert i systemprompten KUN den turen.
Aldri stabling: klassene er gjensidig utelukkende. Ruter-bom → ingen blokk →
naken løkke (dagens adferd). Utkast til katalog i del 6.

Dette løser konflikten som har stått ubesvart siden 2026-07-28: metoderegler
virker (målt), men regex-triggere og permanent prompt-stabling er forbudt
(målt skadelig). Semantisk ruting med eval-port er den sanksjonerte utløseren
som manglet.

### 5.2 Tanken er narrasjon OG relevansanker

Large 3 kan levere prosa og verktøykall i samme completion — transporten
fanger begge i dag, og prosaen KASTES (verifisert i sse.go/engine-løypene).
v6 gjenbruker den to steder, gratis:

1. **Narrasjon.** Prosaen streames som arbeidssteg («Selskapet er en
   nisjeimportør mot horeca — leter etter tilsvarende, ikke volumhusene»).
   Brukeren ser ekte tenkning live i stedet for skriptede fraser. Filtrert
   av eksisterende junk-/ekko-vakter, merket som steg, aldri som svar.
2. ~~**Relevansanker.**~~ FORKASTET etter måling (probelogg kjøring 8):
   å rangere utdragene mot spørsmål + tanke i stedet for spørsmålet alene
   endret 1 av 32 topputdrag. Cosinus steg, men rangeringen sorterer kun
   på rekkefølge, så en jevn løftning velger samme utdrag. Mekanismen er
   fjernet i stedet for beholdt som ubevist. Tanken har fortsatt målt
   verdi — turer med tanke brukte halvparten så mange søk — men verdien
   kommer av at modellen TENKER, ikke av at vi gjenbruker teksten.

Målt risiko som må avkreftes i probe P1: at prosa-før-kall glir over i
planlegging-uten-handling. Mottiltak er utfør-regelen som allerede fjernet
denne feilen, nå med eksplisitt «tenk kort høyt, kall verktøyene i SAMME
svar».

### 5.3 Budsjetter er metodeegenskaper

I dag: globale konstanter (4 søk, 3 hentinger, 6 runder) — samme tak for
«hva koster diesel» og «kartlegg konkurrentene». v6: taket følger metoden
(del 6). Koden håndhever som i dag (kvote i verktøyutføreren, ærlig
kvotemelding). Et oppslag som prøver søk nummer to får beskjed om å levere;
en relasjonsresearch har rom til profil + segment + verifisering.

### 5.4 Leveransekontrakten

Kjerneprompten beholder kun stemme og korthet (som i dag). Formkravene som
er universelle — konklusjon først, tall kun med kilde, aldri liste — står
der allerede. De klassespesifikke (negativ avgrensning, beslutningsregel,
skjerpingsspørsmål) bor i metodeblokkene. Skjerpingsspørsmålet har en
deterministisk vakt: maks ett, og bare når svaret alt inneholder et
erkjent forbehold — håndheves i kode (spørsmålstegn-telling + forbeholds-
sjekk er observerbart), aldri ved å be modellen «vurdere behovet».

### 5.5 Selvkritikk uten dommer

Challenge-kall er målt dødt. v6 legger skeptiker-testen INN i
leveranseinstruksen («hvilken påstand ville en kritisk leser angrepet —
dekker kildene den? Hvis ikke: snevr inn påstanden») — null ekstra kall.
Koden backer med det observerbare: tallkontroll, navnekontroll, og maks én
omskriving ved harde avvik. Aldri mer enn én.

### 5.6 Gulvene porteres uendret

Bevist kode flyttes inn som den er: aggregering >20 rader, typede
db-utfall + ærlig tomt-svar, nærmeste-navn-redning, innsiktslag,
neste-steg-lag, tabellgaranti, kildenote, historikk-kapp, samtaleutdrag,
søke-cache, kvotehåndhevelse i verktøyene, junk-/ekko-filtre, graceful
degradering. Dette er ikke arkitektur, det er gulv — og de er målt.

To nye gulv fra probene: (1) fet skrift og overskrifter strippes
deterministisk i leveransen — modellen bolder kandidatnavn uansett
instruks; (2) research- og anbefalingsflyter får eget svarbudsjett
(MaxChars ~800) slik at kompresjonsgulvet ikke amputerer svarkontraktens
tre deler. Navnekontrollen skal dekke avgrensningssetningen — probene
viste at kravet om negativ avgrensning ellers drar uverifiserte navn fra
hukommelsen inn i nettopp den setningen.

## 6. Metodekatalogen (utkast — probes i P3)

Hver blokk er generell: ingen bransje, ingen entitet, ingen eksempler.
Norsk, imperativ, 300-600 tegn. Endelig ordlyd settes etter P3-proben.

**oppslag** (ferskt/spesifikt faktum)
Budsjett: 1 søk, 0 hentinger, 2 runder.
> Dette er et faktaoppslag. Verifiser med ett søk selv om du tror du vet
> svaret — ferskhet slår hukommelse. Én autoritativ kilde holder. Svar med
> faktumet og tidspunktet det gjelder for. Ikke utred.

**relasjonsresearch** (konkurrenter, alternativer, sammenlignbare, posisjon)
Budsjett: 4 søk, 4 hentinger, 6 runder. Tekst = v3, probet i to runder:
> Svaret avhenger av hvem subjektet ER. Arbeidsrekkefølge: (1) Profiler
> subjektet fra nærmeste kilde — interne data/kunnskap når subjektet er
> brukerens egen virksomhet, aktørens egne sider ellers. (2) Si i
> arbeidsnotatet hvilket kriterium relasjonen krever her, utledet av det
> brukeren skal beslutte. (3) Let etter kandidater INNENFOR det segmentet,
> og les kandidatenes egne sider (fetch_url) før du feller dom om
> portefølje eller posisjon — søkeutdrag og topplister er ikke evidens.
> SVARKONTRAKT, alle tre delene: (a) kandidatene i løpende tekst med hver
> sin korte begrunnelse mot kriteriet; (b) én setning som avgrenser mot
> aktørene som IKKE kvalifiserer — navngi kun aktører som står i kildene
> eller samtalen, ellers beskriv kategorien uten navn; (c) hvis profilen
> har et konkret hull som begrenser svaret: avslutt med ETT spørsmål om
> akkurat det hullet — spørsmålet er en del av leveransen, ikke vegring.
> Tall og fakta om kandidater KUN ordrett fra kildene.

**anbefaling** («hva bør vi bruke/kjøpe/velge»)
Budsjett: 3 søk, 4 hentinger, 6 runder. Tekst = v2, probet:
> Etabler behovet fra samtalen og intern kontekst før du leter. Søk etter
> kategorien og segmentet, aldri «beste X»-fraser — topplister er ikke
> evidens. Les leverandørens EGEN side (fetch_url) før du anbefaler. Lever
> ÉN anbefaling: hvorfor den passer akkurat denne situasjonen, med pris og
> innhold KUN ordrett fra kilden — har du ikke lest prisen, ikke oppgi den;
> si hvor den finnes. Avslutt med den ENE beslutningsregelen som ville
> snudd valget: bygger den på noe kildene sier, si det; ellers merk den som
> din vurdering. Aldri en meny, aldri diktede terskler.

**analyse** (interne tall, hybrid intern×ekstern)
Budsjett: db fritt, 2 søk, 1 henting, 6 runder.
> Hent dataene med verktøyene; koden regner statistikk du skal bruke
> ordrett. Skil tre ting i svaret: hva tallene viser, hva de ikke viser,
> og hva som ville endret konklusjonen. En påstand om sammenheng eller
> forskjell tallfester BEGGE sider. Finnes ikke formen du ønsket, bruk
> nærmeste form som finnes og si det i én bisetning.

**skapende** (idéer, navn, utkast, tekst)
Budsjett: 0 søk, 0 hentinger, 1 runde.
> Påfunn er leveransen. Lever ferdig arbeid med ett tydelig førstevalg,
> aldri prosess. Her er varianter lov.

**samtale** (småprat, mening, meta)
Budsjett: 0 verktøy, 1 runde.
> Ingen verktøy. Svar kort med husets stemme. Er det et reelt behov bak
> småpraten, pek på det i én setning.

Ruting: eksisterende registerarkitektur får metodeklasse som ny dimensjon —
`data_question`→analyse, `web_fact`→oppslag, `smalltalk`→samtale finnes alt;
nye intents `research_relation` og `recommendation` (eksempelytringer på
tvers av mange domener) skiller de to research-klassene fra fri chat.
Klebrighet som i dag: elliptisk oppfølging («og i Sverige?») arver forrige
metode. Lav sikkerhet → ingen metode → naken løkke. Intent-evalen utvides
med de nye klassene og forblir port.

## 7. Scenariokjøringer (forventet adferd, verifiseres i prober)

1. **Relasjonsklassen** («finn våre direkte konkurrenter», vilkårlig
   bransje): ruter → relasjonsresearch. Tanke: «trenger subjektets profil
   først». Verktøy: kunnskapslag/db eller fetch av egne sider → tanke
   navngir segment og kriterium (streames som steg) → segmentsøk rangert
   mot uttalt hensikt → svar: kandidater m/begrunnelse, negativ avgrensning,
   ev. ett skjerpingsspørsmål. 3-5 kall.
2. **Oppslag** («hva er styringsrenten»): ett søk, ett svar med dato.
   2 kall, ingen metodeoverhead utover ~100 tokens blokk.
3. **Analyse** («korrelasjon strømpris×salg»): db + serie/søk, Pearson i
   kode, svar tallfester begge sider + hva som ville endret bildet.
4. **Anbefaling** («trenger et system for X»): behov fra samtale, kandidat-
   sider leses helt, ÉN anbefaling + beslutningsregel.
5. **Tomt aggregat** (navnebom): typet utfall → ærlig «ingen rader som
   matcher», nærmeste-navn-redning. Aldri «0 kroner».
6. **Oppfølging** («og i Sverige?»): klebrig metode, subjektprofil bæres i
   samtalen, ingen gjentatte søk (cache).
7. **Småprat**: 1 kall, husets stemme, null verktøy.
8. **Uklart** («sammenlign oss med dem», ingen referent): unclear-flyt som
   i dag — ett oppklarende spørsmål, null søk.

## 8. Hva v6 IKKE har, med grunn

- Ingen goalstate/mission/attestation/ledger-skjemaer (destabiliserer
  modellen, måler protokoll i stedet for kvalitet).
- Ingen dommer-/challenge-kall (8/16 feil, 43 s, selvforsvar).
- Ingen separat planleggerkall (kost på hver tur, dobbel planlegger).
- Ingen regex-/nøkkelordtriggere for semantikk (fanger ikke omskrivinger).
- Ingen prompt-stabling (målt verre svar).
- Ingen formuleringsvakter (modellen omgår ordlyd; utløs på fakta).
- Ingen retry-kaskader (maks én omskriving, på observert avvik).
- Ingen fallback-modeller (én modell er et bevisst valg; ærlig nede-melding).
- Ingen mocks i evaluering (falsk trygghet, målt).

## 9. Probeplan — ekte modell, falsifikasjon satt på forhånd

Harness: `cmd/v6probe` — kjører promptsett mot upstream med ekte
web_search/fetch_url (internal/search), logger fulle transkript + usage +
latens per kjøring til `probe_runs/<ts>/`. Ingen mocks. Dev-sett og
holdt-tilbake-sett er adskilt; holdt-tilbake kjøres først ved A/B-aksept og
leses ikke under design (anti-overtilpasning, samme regel som plan-eval).

- **P1 — tenk-så-handle i samme completion.** 8 dev-prompts, instruks med
  kort-tanke-så-kall. Faller hvis: >1/8 leverer plan uten verktøykall, eller
  prosa-før-kall er tom/støy i >3/8.
- **P2 — reasoning-feltet på Large 3: AVGJORT.** La Plateforme svarer 422
  på feltet (målt i prod). Tenk-prosaen i samme completion er
  resonneringskanalen. Ingen videre måling.
- **P3 — metodeblokk-effekten.** 10 dev-prompts i relasjons- og
  anbefalingsklassen på tvers av ulike domener, A (naken) mot B (metode).
  Leses manuelt mot anatomi-kriteriene (profil-først, kriterium, negativ
  avgrensning, skjerpingsspørsmål). Faller hvis B ikke er tydelig bedre på
  ≥7/10, eller B introduserer nye feil (lengre, listete, tregere enn 1,5x).
- **P4 — tanke som narrasjon.** Prosaen fra P1/P3 leses: er den kort, norsk,
  informativ nok til å streames? Faller hvis >3/10 er engelsk, rablende
  eller avslører intern instruks.
- **P5 — ruterutvidelsen.** Nye intents med eksempler; eksisterende
  intent-eval + nye linjer på tvers av domener. Faller hvis samlet eval
  synker under dagens 92,4 %, eller nye klasser treffer <85 %.
- **P6 — leveransekontrakt under press.** 6 dev-prompts der fristelsen er
  meny/liste/hedging. Faller hvis >2/6 bryter formen.

Hver probe: transkriptene arkiveres, dommen skrives i
`docs/MOTOR-V6-PROBER.md` med rå tall FØR konklusjon. Faller en probe,
revideres designet der — ikke prompten mot enkeltcaser.

## 10. Implementasjonseksperiment (etter grønne prober)

Minste eksperiment: v6-løkka bak `ENGINE=v6` med KUN metodeklassene
oppslag + relasjonsresearch + samtale, gulvene portert, resten til legacy.
A/B mot dagens motor på et holdt-tilbake-sett (20 prompts, nye domener,
skrevet av andre enn den som implementerte, ulest til kjøring).

Aksept (alle må holde):
- Relasjonsklassen: anatomi-kriteriene oppfylt i ≥8/10 holdt-tilbake,
  mot dagens baseline lest blindt side om side.
- Oppslag: uendret kall-tall og latens ±10 %, tokens ±10 %.
- Null protokoll-lekkasje i samtlige transkript.
- Null diktede tall/navn som passerer tallkontrollen.
- Intent-eval grønn, hele testsuiten grønn.

Faller aksepten: feilmønstrene dokumenteres og designet revideres. Ingen
deploy av en motor som ikke slår baseline ved blindlesing.

## 11. Tokenøkonomi (anslag — merkes som anslag til P-probene har målt)

- samtale/skapende/oppslag: som i dag ±metodeblokk (~100-150 tokens én gang
  per tur).
- relasjonsresearch/anbefaling: 3-5 kall mot dagens 1-6, men målrettet:
  profil-henting (1-2) erstatter 2-3 brede søk med full kontekstlast
  (~5k tokens/søk re-sendt hver runde). Netto forventet likt eller lavere
  enn dagens research-turer — måles i P3 med usage-tall.
- Tanke-prosaen er tokens vi allerede betaler og kaster; v6 gjenbruker dem.

## 12. Integrasjon

- Ny fil `internal/api/enginev6.go` + `methods.go` (katalog + budsjetter,
  data ikke kode: metodetekstene som konstanter, budsjetter som struct).
- Ruterutvidelse i `internal/intent` (registry + flows får Method-felt).
- `ENGINE=v6` i config; av = dagens vei urørt.
- Utfører-flyter, paneler, connector, design: uendret.
- Deploy-rekkefølge: prober → implementasjon bak flagg → A/B lokalt →
  Jacob leser blindsammenligningen → først da prod.
