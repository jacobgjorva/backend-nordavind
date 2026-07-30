# Motor v6 — A/B-aksept mot holdt-tilbake-settet

Rå tall før konklusjon, som probeloggen.

## Oppsett

- Sett: `cmd/v6ab/testdata/holdout.jsonl`, 20 prompts i domener som ikke er
  brukt under utviklingen. Skrevet og committet FØR første kjøring
  (`619a52b`), etter at design og metodetekster var låst.
- Begge sider kjørt samme dag, mot samme binær og samme sett. Eneste
  forskjell: `ENGINE=v6`.
- Backend lokalt med `INTENT_ENGINE=on`, `AUTH_REQUIRED=false`.
- Rådata: `ab_runs/A` (legacy), `ab_runs/B` (v6). Blindlesefil:
  `ab_runs/BLINDLESING.md`, fasit i egen fil.

**Ærlig svakhet:** settet er skrevet av den som bygde motoren. Domenene er
valgt utenfor alt utviklingsmateriale, men en uavhengig forfatter ville
vært bedre.

## Kvantitativt

```
              A (legacy)   B (v6)
total tid        332 s      224 s     -32 %
snitt steg        4,0        3,8
snitt kilder      5,2        5,0
snitt svarlengde  210 t      292 t

per klasse (snitt tid)
  relasjon       28,2 s     22,2 s
  anbefaling     13,9 s      5,4 s
  oppslag        14,4 s      9,2 s
  samtale         0,3 s      0,3 s
  uklar           3,3 s      2,4 s
```

Ingen feil eller tomme svar på noen av sidene. Ingen protokoll-lekkasje.

Budsjettene per metode gjør jobben: v6 er raskere overalt uten å hente
færre kilder der det trengs. Anbefaling faller mest (13,9 → 5,4 s) fordi
metoden stopper letingen når kandidaten er funnet, i stedet for å bruke
opp en global kvote.

## Kvalitativt: svarkontrakten leveres ikke

Relasjonsklassens svarkontrakt har tre deler (kandidater med begrunnelse,
negativ avgrensning, skjerpingsspørsmål ved profilhull). Målt på de sju
relasjonsoppgavene:

```
                       A (legacy)   B (v6)
negativ avgrensning       0/7        1/7
skjerpingsspørsmål        0/7        0/7
```

I designprobene (probelogg kjøring 3) var de samme tallene 4/6 og 6/6.

## Årsaken: modellen, ikke metoden

Probene kjørte `mistral-large-2512` via La Plateforme. A/B-en kjørte
`mistral-medium-3.5-128b`, fordi det er modellen `main` ruter til.

Kontrolltest, samme motor og samme metodetekst, kun modellen byttet — de
samme holdout-oppgavene gjennom `cmd/v6loop` på Large 3:

```
                    medium (A/B)   Large 3 (kontroll)
negativ avgrensning     1/7             3/3
svarlengde             ~290 t          ~1 500 t
```

Eksempel, h01 på Large 3, siste setning:

> «De fleste andre sagbrukene i Innlandet leverer generell trelast til bygg
> og anlegg, men uten spesialisering på verneverdige bygg eller
> tradisjonelle bearbeidingsmetoder. De er derfor ikke direkte
> konkurrenter til dere.»

Samme oppgave på medium (v6, A/B-en) ga en oppramsing av to navn uten
avgrensning.

Dette er det samme mønsteret som er dokumentert tidligere i prosjektet:
medium er en god skrivemotor når koden gir den ferdigregnet grunnlag, men
den følger ikke en flerdelt svarkontrakt. Large 3 gjør det.

## Dom

**Delvis akseptert. Ikke klar for prod på dagens modell.**

Det som holder mål:
- v6 er 32 % raskere med uendret kildedekning, på tvers av alle klasser.
- Null motorfeil, null lekkasje, null tomme svar på 20 usette oppgaver.
- Metoden og budsjettene virker som mekanisme — det er dokumentert både i
  hastigheten her og i anatomien på Large 3.

Det som ikke holder mål:
- Anatomien — som er hele begrunnelsen for v6 — leveres ikke på
  `mistral-medium`, modellen `main` bruker i dag. På den modellen er v6
  raskere, men ikke vesentlig bedre.

Akseptkravet fra designet (del 10) var «relasjonsanatomi ≥8/10 ved
blindlesing». Det er ikke innfridd på medium. Det ER innfridd på Large 3 i
kontrolltesten, men den er kjørt utenfor A/B-oppsettet og på tre oppgaver,
ikke ti.

## Neste steg, i rekkefølge

1. **Port leverandørrutingen** (Large 3 hos La Plateforme) til `motor-v6`.
   Koden finnes og er målt på `engine-experiment`: ~50 linjer i
   `server.go` + router-konstantene. Uten den kan v6 ikke vise det den er
   bygget for.
2. **Kjør A/B på nytt med Large 3 på begge sider.** Da er modellen
   konstant og motoren variabelen, slik den skal være.
3. **Blindlesing** av `ab_runs/BLINDLESING.md` før fasiten åpnes.
4. Først deretter en prod-beslutning.

Blindlesefila fra denne runden er laget og kan leses nå, men den måler v6
på feil modell. Anbefalingen er å vente til punkt 2.
