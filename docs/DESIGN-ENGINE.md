# Designmotoren (forslag)

Én motor for alt brukeren lager visuelt: presentasjoner, flyers, kampanjer,
plakater. Ikke tre verktøy — ett verktøysett over én datamodell, der
dokumenttypen er en JSON-fil.

## Hvorfor omskriving

Dagens deck-flyt koster mye fordi den bygger **én slide per verktøykall**.
Hvert kall gir en ny runde der hele historikken (system + alle tidligere
tool_calls + tool-svar) sendes på nytt. Ti slides er ti runder med voksende
kontekst, pluss en gjennomgangsrunde på toppen — kostnaden vokser kvadratisk
med antall slides, ikke lineært.

Selve ideen — ferdig designede kitt i JSON, modellen fyller faste felt — er
riktig og beholdes. Det som endres er hvor mange ganger vi betaler for den.

## Datamodell

Ett dokument, uansett type:

```json
{
  "kit": "noir",
  "type": "deck",
  "surfaces": [
    { "id": "s1", "layout": "title", "fields": { "title": "…", "image": "bg-1" } }
  ]
}
```

- **surface** = én flate: en slide, en flyer-side, en plakat. Formatet
  (16:9, A4, kvadrat) kommer fra kittet, aldri fra modellen.
- **fields** = kun det kittets layout definerer. Ukjente felt avvises i kode.
- **id** = eid av koden. Modellen finner aldri på id-er.

Eksisterende decks leses uendret: `slides` er alias for `surfaces`.

## Kitt-filen

Ett kitt = én dokumenttype med ett uttrykk. `internal/design/kits/<navn>.json`:

```json
{
  "name": "noir",
  "type": "deck",
  "format": { "ratio": "16:9", "width": 1920 },
  "tokens": { "bg": "#000", "text": "#fff", "sans": "…" },
  "palette": ["#6b8afd", "…"],
  "assets": ["bg-1.jpg", "…"],
  "layouts": [
    {
      "key": "title",
      "use": "åpningsflate",
      "slots": { "title": "tekst, 3-7 ord", "image": "bilde!" },
      "blocks": [ … ]
    }
  ]
}
```

- `slots` er kompakt: felt → hint på én linje, `!` markerer påkrevd. Dagens
  katalog er ~880 tokens; denne formen lander på ~300.
- `blocks` er rendrings-DSL-en som allerede virker (group/text/md/image/
  chart/…) og tolkes kun av frontend.
- Ny dokumenttype = ny fil. Ny flate-type = ny layout i fila. Ingen kode.

## Verktøy (tre, ikke flere)

1. **compose** — hele dokumentet i ETT kall: en liste av flater med layout og
   felter. Brukes ved nybygg og ved «bygg om alt». Én runde inn, én ut.
2. **patch** — én flate, felt-nivå. AI-redigering og brukerens dobbeltklikk i
   canvaset går samme kodesti, så manuelle rettinger aldri overskrives.
3. **restyle** — kitt, tema eller token-overrides. Aldri layout.

Bygg-modus gir `tool_choice: compose`, redigering gir `patch`/`restyle`.
Koden velger, ikke modellen.

## Token-økonomi

Det er her gevinsten ligger:

- **Én runde ved nybygg.** Ingen tool-svar-kjede, ingen resending. Anslag:
  ~1,5k inn + ~2k ut mot dagens 3-4 runder à 8-25k inn.
- **Katalog kun ved compose.** Ved patch sendes bare den aktuelle layouten
  og flatens nåværende innhold, ikke hele kittet.
- **Databaseskjema kun når dokumentet faktisk har datablokker.** I dag følger
  hele skjemaet med selv til en presentasjon om aper. Har dokumentet ingen
  graf- eller KPI-flater, sendes ingen SQL-kontekst; ber brukeren om tall,
  legges skjemaet på fra da av.
- **Ingen egen gjennomgangsrunde.** compose leverer hele disposisjonen samlet,
  så rekkefølgen bestemmes i ett drag i stedet for å repareres etterpå.

## Hva koden eier (aldri modellen)

- id-er, rekkefølge og lagring
- validering mot kittet: ukjent layout eller slot avvises med en presis
  feilmelding tilbake til modellen, i stedet for å lagre søppel
- rendering, typografi, farger, format
- live data: SQL kjøres ved visning, aldri lagrede tall

## Design-chat: en egen kategori, ikke et lag over chatten

Design får sin egen kategori i sidebaren, slik agenter har det i dag.
`/design` oppretter en design-chat og lander brukeren der — samme mønster som
`/agent`. Lerretet ER siden, ikke et panel som spretter opp over en pågående
samtale.

Layout: lerretet i midten, flate-listen langs kanten, chat-tråden som
instrukslinje ved siden av. Fordi siden er dedikert, kan verktøylinjen bære
det som bare gir mening her — forhåndsvisning, presentasjonsmodus, opplasting
av egne bilder, temabytte, eksport — uten å forstyrre vanlig chat.

En design-chat husker dokumentet sitt, så brukeren kommer tilbake til en
plakat på samme måte som til en agent.

## Hvordan brukeren velger type og uttrykk

**Én inngang.** `/design` åpner en ny design-chat med et galleri som første
skjerm: dokumenttypene (presentasjon, flyer, kampanje) med ekte miniatyrer
rendret fra kittene selv. `/design flyer` hopper rett dit for den som vet hva
den vil. Ingen intent-gjetting på type — brukeren peker.

**Bytte underveis.** Samme galleri ligger i verktøylinjen. `restyle` beholder
innholdet og bytter uttrykk, noe som forutsetter at kittene deler et felles
vokabular av layout-nøkler (`title`, `bullets`, `image`, `chart` …). Mangler
et kitt en nøkkel, faller flaten til nærmeste slektning i det kittet — regelen
håndheves i kode, ikke av modellen.

**Når ingenting passer.** To nivåer av frihet: farger, fonter og bilder kan
overstyres fritt oppå et hvilket som helst kitt (det dekker merkevare og
«gjør den lysere»). Et helt nytt uttrykk er en ny JSON-fil — vår jobb, ikke
modellens. Lar vi modellen komponere layout fritt, mister vi nettopp det som
gjør resultatet proft, og vi er tilbake til å betale tokens for design vi
allerede har designet.

## Frihet uten fragmentering

Brukerens frihet ligger i innhold og i antall kitt, ikke i at modellen får
tegne fritt. Vil noen ha flyer: `flyer.json` med A4-format og sine layouts.
Kampanje: `campaign.json` med flere flater i samme uttrykk. Motoren, canvaset,
patch-API-et og rendereren er de samme — og et nytt uttrykk koster en JSON-fil,
ikke en ny kodesti.

Merkevare per kunde blir samme mekanisme: et kitt med tenantens farger,
fonter og bilder.

## Migrasjon

1. `internal/deck` → `internal/design`, `slides` → `surfaces` med alias for
   lagrede decks.
2. `set_slide`/`set_deck` → `compose`/`patch`/`restyle`.
3. Frontend-rendereren beholdes som den er; den leser allerede blocks fra
   kittet.
4. noir blir første kitt. Deretter én ny kitt-fil som bevis på at det er
   gratis å legge til (flyer).
5. Design-chat som egen kategori i sidebaren; `/presentasjon` blir en snarvei
   til `/design deck`, og canvas-over-chat fjernes.

## Åpne valg

- Om `compose` skal kunne kalles med en delmengde flater («bygg om seksjon 2»)
  eller om det alltid er hele dokumentet.
- Om bildegenerering skal inn som en fjerde verktøytype senere, eller om
  assets alltid kommer fra kittet og opplastinger.
