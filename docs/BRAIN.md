# Den interne hjernen (forslag)

Målet er ikke bedre gjenfinning. Målet er at grafen skal kunne SLUTTE noe:
koble fakta som aldri har stått i samme setning, se at et mønster gjentar
seg, og si fra om det uoppfordret. Dagens lag kan hente. Det kan ikke tenke.

Skrevet 27. juli 2026. Ingenting bygges uten godkjenning.

## Hvorfor dagens modell ikke kan nå dit

En node i dag er en setning med en tittel:

    type: regel
    title: "Ukeplanlegging"
    summary: "Mandagsmøte for å gjennomgå forrige uke og planlegge prioriteringer"

Den setningen kan gjenfinnes, men den kan ikke brukes til å svare «hvilken
dag har jeg møte med sjefen min?», fordi den ikke VET hvem som deltar. Det er
ikke en hentingsfeil — informasjonen finnes ikke i modellen vår. Tre
strukturelle mangler følger av det:

1. **Ingen aktører.** Personer, roller, kunder, produkter og systemer er ikke
   ting i grafen; de er ord inne i en tekst. Da kan ingenting kobles på
   «hvem», og et spørsmål om en person treffer bare hvis personen tilfeldigvis
   er nevnt ordrett.
2. **Ingen tid.** Alt er like sant for alltid. «Vi bruker Neon» fra i fjor og
   «vi gikk over til Postgres» fra i går ligger side om side, begge accepted.
   En hjerne må vite hva som gjelder NÅ og hva som gjaldt før.
3. **Kanter uten jobb.** De skrives (`relations` i uttrekket), men kobles på
   eksakt tittelmatch og treffer nesten aldri. Selv når de treffer, brukes de
   bare til ett hopp med lavere vekt i hentingen. Ingen slutning, ingen
   telling, ingen mønstre.

Dette er ikke lapper som kan repareres. Modellen må endres.

## Kjernen: fakta som påstander, ikke setninger

Erstatt «node med en oppsummering» med en **påstand** — subjekt, relasjon,
objekt, med tid og kilde:

    (Jacob) —[har fast møte med]→ (Kari)     når: ukentlig, mandag
                                             kilde: chat 4a2, 26.07.2026
                                             sikkerhet: 0.9, gyldig: nå

Entitetene (Jacob, Kari) er egne noder som lever videre og samler påstander.
Da faller «møte med sjefen min» ut av grafen som en ren traversering: Jacob
—[har sjef]→ Kari, Jacob —[har fast møte med]→ Kari (mandag). To fakta som
aldri har stått i samme setning, satt sammen til ett svar.

Konkret skjema (Postgres er allerede master):

```sql
entities   (id, tenant_id, kind, name, aliases[], embedding, first_seen, last_seen)
claims     (id, tenant_id, subject_id, predicate, object_id, object_text,
            qualifiers jsonb,        -- tid, sted, frekvens, terskel
            confidence real,
            valid_from, valid_to,    -- NULL = gjelder nå
            source_kind, source_id, stated_by, created_at)
mentions   (claim_id, note_id)       -- sporet tilbake til råteksten
```

- `entities.kind`: person, rolle, kunde, produkt, system, sted, prosess,
  dokument. Aliaser fanger «sjefen», «Kari H.», «KH».
- `claims.predicate` er et LUKKET vokabular (se under) — fri tekst her ville
  gjort grafen usøkbar igjen.
- `object_text` brukes når objektet er en verdi («48 timer», «mandag»), ikke
  en entitet.
- Lappe-skuffen beholdes uendret som råtekst-laget for sitering. Påstandene
  er destillatet, lappene er beviset.

## Predikat-vokabularet

Lukket, lite, utvidbart som JSON — samme filosofi som design-kittene:
koden eier formen, modellen fyller innholdet.

    Struktur:  er en, er del av, eier, jobber i, har rolle
    Relasjon:  har sjef, rapporterer til, samarbeider med, er kunde av,
               er leverandør til, er ansvarlig for
    Handling:  møter, leverer, godkjenner, bruker, erstatter
    Egenskap:  har frist, har terskel, koster, holdes på, gjelder for

Et uttrekk som ikke passer inn i vokabularet lagres som en vanlig lapp (som i
dag) og telles. Predikater som stadig etterspørres, legges til bevisst — ikke
av modellen i farten.

## Uttrekket: fra «tre noder» til påstander

Dagens uttrekk kjører på hver utveksling, med en billig modell, og gir maks 3
løsrevne noder. Nytt uttrekk gjør tre ting i ett kall:

1. **Finner entiteter** i teksten og knytter dem til eksisterende via
   embedding + alias, ikke eksakt tittel. Ny entitet opprettes bare når
   likheten er under terskel (målt, ikke gjettet).
2. **Bygger påstander** med predikat fra vokabularet, med kvalifikatorer
   (tid, frekvens, terskel) som egne felt.
3. **Merker motstrid**: hvis en ny påstand har samme subjekt+predikat som en
   gjeldende, settes den gamle til `valid_to = nå` i stedet for å ligge ved
   siden av. Historikken beholdes, sannheten er entydig.

«Jeg har ukentlig møte med Kari, som er sjefen min» blir da tre påstander og
to entiteter — ikke én setning i en tekstboks.

## Hentingen: fra likhet til traversering

Dagens vei (vektor + BM25 + RRF + ett hopp) beholdes for råtekst. Over den
legges et påstandslag:

1. **Entitetsgjenkjenning i spørsmålet.** «sjefen min» → rolle-oppslag for
   den innloggede brukeren → entitet.
2. **Målrettet traversering.** Hent påstandene rundt entitetene, 1-2 hopp,
   filtrert på gyldig-nå.
3. **Komprimert kontekst.** Påstander er korte av natur: 20 påstander koster
   mindre enn 3 tekstlapper og er langt mer presise. Råtekst hentes bare når
   spørsmålet trenger detaljer eller sitering.

Effekten på tokens går NED, ikke opp — samme mekanikk som i designmotoren:
strukturert innhold er billigere enn prosa.

## Prosedyrer og dokumenter

En prosedyre er en ORDNET sekvens. Presser man den inn i trippelformen,
mister man rekkefølgen — «steg 3 kommer etter steg 2» som tre løse påstander
er verdiløst. Prosedyrer blir derfor egen nodetype med stegene intakt:

```sql
procedures (id, tenant_id, name, trigger, steps jsonb, source_kind, source_id,
            valid_from, valid_to)
-- steps: [{"n":1,"do":"…","owner_id":"…","deadline":"48t før levering"}, …]
```

Koblingen til resten skjer med vanlige påstander, så prosedyren ikke blir en
øy: `(prosedyre) —[gjelder for]→ (kunde Vestland Fisk)`,
`(Ola) —[er ansvarlig for]→ (prosedyre)`, `(prosedyre) —[erstatter]→
(gammel prosedyre)`. Da svarer «hvordan gjør vi det for Vestland Fisk?» med
riktige steg i riktig rekkefølge, og «hvem eier den rutinen?» med Ola — uten
at noen har skrevet begge deler i samme setning.

Dokumenter beholder rollen de har i dag: råtekst i lappe-skuffen, med
dok-noden over. Det nye er at uttrekket kjøres OVER dokumentet ved
opplasting, slik at prosedyrer, regler og påstander destilleres ut med
dokumentet som kilde. Bitene blir liggende for sitering — spør noen om
detaljer eller vil se ordlyden, hentes den fra kilden, ikke fra destillatet.

Det gir tre nivåer som utfyller hverandre: påstander for slutning,
prosedyrer for framgangsmåte, råtekst for bevis. Mønsterjobben får samtidig
noe å måle mot — en faktisk hendelse som bryter med prosedyrens steg er
nettopp et avvik verdt å si fra om.

## Det som gjør den til en hjerne: mønsterjobben

Alt over gir presisjon. Mønstre krever noe eget: en jobb som leser grafen når
det er stille, ikke midt i en samtale.

- **Samforekomst.** Entiteter som stadig opptrer i samme påstander uten å være
  koblet → foreslå kant («Ola og Vestland Fisk nevnes i 14 avvik — er Ola
  ansvarlig for den kunden?»).
- **Gjentakelse over tid.** Samme predikat med jevne mellomrom → utled en
  rytme («klage fra denne kunden hver 3. uke siden mars»).
- **Avvik.** En påstand som bryter et etablert mønster → flagg («alle andre
  leveranser klareres 48 t før; denne ble klarert 6 t før»).
- **Hull.** Entitet som mangler en påstand alle sammenlignbare entiteter har
  («ingen av de 12 kundene mangler kontaktperson — bortsett fra denne»).

Funnene blir FORSLAG med begrunnelse og lenke til påstandene de hviler på,
aldri automatisk sannhet. De vises der de hører hjemme: i grafen, og som en
kort melding når de er sterke nok. Terskelen settes av evalen, ikke av
magefølelse.

Kostnad: jobben kjører per tenant på natten, leser lokalt, og bruker modellen
bare til å formulere de få funnene som passerer terskelen.

## Hva koden eier (aldri modellen)

- entitets-identitet og sammenslåing (terskler, målt)
- predikat-vokabularet
- tidslinjen: hva som er gyldig nå, hva som er historikk
- sporbarhet: hver påstand peker på lappen og samtalen den kom fra
- mønsterjobbens statistikk (telling, ikke gjetting) — modellen formulerer,
  koden regner

## Evalen først (samme rituale som intent-motoren)

Ingenting av dette bygges uten en fasit å måle mot. Tre sett:

1. **Gjenfinning** (finnes delvis): spørsmål → forventet lapp/påstand.
2. **Slutning**: spørsmål som krever to eller flere påstander koblet
   («hvilken dag har jeg møte med sjefen min?»). Dette settet finnes ikke i
   dag, og det er nettopp det hjernen måles på.
3. **Mønster**: en syntetisk graf med plantede mønstre og støy — jobben skal
   finne de plantede og ikke finne de plantede fellene.

Hvert steg under slippes bare med grønn eval.

## Byggerekkefølge

1. **E1 — Evalsettene** (slutning + mønster). Uten dem er resten synsing.
2. **E2 — Entiteter og påstander i skjemaet**, med backfill fra dagens noder:
   eksisterende summary-tekster kjøres gjennom det nye uttrekket én gang.
   Ingen data går tapt; lappene står urørt.
3. **E3 — Nytt uttrekk** (entiteter + påstander + motstrid), bak flagg, med
   dagens uttrekk som fallback til evalen er grønn.
4. **E4 — Traverserende henting** ved siden av dagens, valgt av kode når
   spørsmålet inneholder kjente entiteter.
5. **E5 — Grafen som editor**: entiteter, påstander og tidslinje synlig og
   redigerbar. Uten dette kan ingen rette opp feil hjernen har lært.
6. **E6 — Mønsterjobben**, sist, når grunnlaget er tett nok til at funnene
   faktisk betyr noe.

## Hva som beholdes fra dagens system

Lappe-skuffen, hybrid henting, FTS, dublettvakten, bruks-tellingen og
governance v2 (bekreftelse ved kilden) er alle riktige og blir stående.
Påstandslaget legges OVER dem — dette er ikke en omstart, det er et nytt lag
som gir grafen noe å resonnere med.

## Ærlig om risikoen

- **Uttrekket blir dyrere.** Entiteter og påstander krever en sterkere modell
  enn dagens billige. Motvekt: det kjører i bakgrunnen, ikke i svarløyfa, og
  hentingen blir billigere.
- **Feil påstander er verre enn ingen.** En gal kant sprer seg gjennom
  traversering. Derfor: sikkerhetsgrad på hver påstand, bekreftelse ved
  kilden for alt som gjelder personer, og eval som port.
- **Mønsterjobben kan bli en støymaskin.** Derfor terskler målt mot et
  syntetisk sett med bevisste feller, og alltid begrunnelse med kilder.
