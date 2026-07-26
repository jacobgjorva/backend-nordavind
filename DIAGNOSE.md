# Ende-til-ende-diagnose, 25. juli 2026

Full stopp etter en dag med enkeltfikser. Dette er den målte helheten og en
prioritert plan. Ingenting under bygges uten godkjenning.

## Hvor tiden faktisk går (målt)

Hver melding går gjennom disse SERIELLE leddene før brukeren ser ett tegn:

1. **Intent-ruting (synkron)**: embedding 150-300 ms + dommer 200-2500 ms på
   usikre treff, pluss i verste fall ETT ekstra embed+dommer for forrige
   melding (sticky-oppslag, målt 1,9 s kaldt lokalt). Typisk 300-800 ms,
   verst 3-5 s.
2. **Kunnskapsoppslag**: eget embedding-kall (~150-300 ms kaldt, cachet ok).
3. **Verktøy-oppsett**: raskt (sqlite), men hver databasespørring åpner NY
   tilkobling mot kundens SQL Server (login ~100-200 ms per spørring).
4. **Modellstart (upstream)**: 0,5-2 s til første token hos Scaleway.
5. **BUFRING — hovedsynderen**: `relayRound(..., false)` buffrer HELE
   modellsvaret før noe vises (for lengdemåling/backstop). Brukeren venter
   altså på hele svartiden + evt. et EKSTRA omskrivingskall (compressAnswer).
   Hybrid-streamingen som ble designet 24. juli ble aldri implementert.
6. **Verktøyrunder**: inntil 3 runder, hver = full bufret modellrunde +
   verktøytid (databasefrist nå 30 s).

Målt lokalt: «Hallo?» kaldt = 2,4 s til første tegn (1,9 s var ruting),
varmt = 0,5 s. I prod kommer punkt 4-6 på toppen; dataspørsmål med treg
kundebase summerer til 30-50 s.

## Feilklassene fra i dag (katalog)

- **A. Pynt/dikting rundt verktøydata**: modellen la navn («Pernod Ricard»)
  og tall inn i svar der kilden kun har ID-er; ved total spørringsfeil diktet
  den hele rader. Rot: sannheten GJENFORTELLES av modellen i stedet for å
  rendres i kode.
- **B. Tvetydige spørringer**: «nyligste rad» uten deterministisk tiebreak
  gir vilkårlig rad blant likestilte datoer.
- **C. Historikk-forgiftning**: gamle feilsvar i samtalen ankret modellen
  (lappet med vasking — bør erstattes av A-løsningen).
- **D. Ruting-kapring**: tekstlikhet kapret samtaler til paneler (nå dekket
  av panel-vern + samtale-regler, men i mange små lag).
- **E. Treg kundedatabase**: sortering uten indeks (>10 s); utsnitt omskrevet
  sargbart, indeks hos kunden gjenstår.
- **F. Modell-lekkasje**: utgåtte modellnavn passerte ruteren (løst med
  allowlist).

## Prioritert plan (bygges i denne rekkefølgen, én om gangen)

- **P1 — Ekte hybrid-streaming**: stream tokens umiddelbart; lengde/backstop
  håndteres i etterkant kun når terskler brytes (avbryt + omskriv, slik
  designet). Fjerner den største OPPLEVDE tregheten for alle svar.
- **P2 — Rutingens hurtigsti**: trivielle korte meldinger (hilsener o.l.)
  hopper over hele motoren; sticky-oppslag skal aldri koste nye nettverkskall
  (kun cache); dommer-kall parallelliseres med kunnskapsoppslaget.
- **P3 — Deterministisk datasvar**: resultatrader rendres som tabell/verdi i
  KODE; modellens rolle reduseres til SQL + kort ledsagertekst validert mot
  celleverdiene. Fjerner klasse A og C strukturelt (vaske-lappen fjernes).
- **P4 — SQL-kontrakt**: generert SQL får alltid deterministisk tiebreak
  (ORDER BY … , dokumentnr) og utsnitt håndheves (på plass); anbefalt indeks
  på sales_date sendes kunden.
- **P5 — Konsolidering + port**: fjern lappelagene P1-P3 overflødiggjør
  (historikk-vask, stamme-dedup), full sim + eval som port, og latensbudsjett
  i flow-sim (ttft-krav per meldingstype).

## Måltall etter P1-P2 (forslag)

Første tegn: smalltalk < 1 s, vanlig svar < 1,5 s, dataspørsmål < 3 s +
spørringstid. Måles i flow-sim på hver kjøring.
