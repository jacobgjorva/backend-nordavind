# Postgres-migreringen — plan v2 (gjennomgått, til godkjenning)

26. juli 2026, revidert etter fallgruve-gjennomgang. Motivasjon: kunnskaps-
hentingen laster i dag ALLE embeddings (4096 dims, ~16 MB JSON per 1000
lapper) inn i Go per melding. Robust løsning er vektorsøk I databasen:
Postgres 16 + pgvector på nordavind-1. Ingenting bygges før planen er
godkjent.

## Viktigste endring fra v1: ingen dimensjonsreduksjon i kritisk sti

v1 la opp til å trunkere embeddings til ≤2000 dims for HNSW-indeks. Det er
migreringens mest risikable steg — og det er IKKE nødvendig nå: pgvector gjør
eksakt søk uten indeks i C, og ved 1 000-10 000 rader er det få millisekunder
(mot dagens hundrevis i Go + JSON-parsing). Derfor:

- Lagres som `vector(4096)`, full presisjon, INGEN indeks i første omgang.
- HNSW (som krever ≤2000 dims, halfvec ≤4000) utsettes til målt behov
  (>50 000 lapper), og dimensjonsvalget tas DA — gated av knowledge-eval.
- Gevinsten nå kommer av at kun topp-30 krysser db-grensa, ikke av indeksen.

## Verifiserte fakta (sjekket i kode/dokumentasjon, ikke antatt)

- Embeddings er 4096 dims (målt i databasen). pgvector-indeksgrenser:
  vector 2000 / halfvec 4000 — derfor utsettelsen over.
- Store-laget: 16 filer, ~3 600 linjer. SQLite-spesifikt er begrenset:
  11 forekomster av `INSERT OR IGNORE/REPLACE` + FTS5 (knowledge.go,
  notes.go). Ingen `LIKE`, ingen `strftime`/`julianday`; `date()` i usage.go
  finnes også i Postgres.
- Postgres har norsk fulltekst-config (`'norwegian'`, snowball) — erstatter
  FTS5. Synken holdes EKSPLISITT i kode som i dag (ingen trigger-magi).

## Fallgruver som håndteres eksplisitt

1. **Samtidighet.** SQLite har én skriver; koden kan stole på det uten å vite
   det. F2 inkluderer en audit av alle les-endre-skriv-løp (innloggingskoder,
   usage-tellere, OAuth-state, live-lenker) → atomiske UPDATE-er eller
   `ON CONFLICT`; aldri stille antatt serialisering.
2. **Tidsstempler.** SQLite lagrer TEXT i UTC; Postgres får `timestamptz`.
   Migreringsscriptet parser og skriver eksplisitt UTC — aldri implisitt
   sone. Verifiseres med stikkprøver på kjente rader.
3. **Upserts.** Alle 11 `INSERT OR …` skrives om til `ON CONFLICT` med
   eksplisitt konflikt-nøkkel (semantikken er IKKE identisk — gjennomgås én
   og én).
4. **FTS-semantikk.** BM25 → `ts_rank` endrer rangering noe. Porten er
   knowledge-eval med SAMME treffkrav som SQLite-baseline (100 %); rødt =
   stopp og juster spørringen, aldri terskelen.
5. **Driftsflate.** Postgres fra PGDG-apt (postgresql-16 +
   postgresql-16-pgvector), kun localhost, egen db-bruker med minimale
   rettigheter, liten connection-pool (VPS-en er beskjeden). Daglig pg_dump —
   og én GJENNOMFØRT prøve-restore før cut-over, ikke bare backup.
6. **Døde kolonner.** agents.category og oppdrags-feltene utelates
   (migrerings-notatet); `mission_activity` beholdes — brukes av spinup.
7. **Cut-over.** Tjenesten STOPPES (ingen skriv under kopiering), migrering
   kjøres, verifisering: radantall per tabell, stikkprøver, innlogging, ett
   dataspørsmål, ett dokumentspørsmål. Rollback = gammel binær mot arkivert
   SQLite-fil. Migreringsscriptet er idempotent (kan kjøres på nytt).

## Faser

- **F1 — Skjema + migreringsscript** (cmd/pg-migrate): les SQLite → skriv
  Postgres, med verifisering innebygd.
- **F2 — Store-laget**: pgx via database/sql, `?`→`$n`, upserts, tsvector,
  vektor-spørring erstatter AcceptedNotes-brute-force. Samtidighets-audit.
- **F3 — Porter**: `go test ./...`, knowledge-eval (100 %), intent-eval,
  manuell røyk-test.
- **F4 — Prod-cut-over** på nordavind-1 etter grønt lokalt løp av F1-F3 mot
  en lokal Postgres.

## Status 26. juli kveld

F1 GRØNN: skjema + pg-migrate verifisert mot ekte lokaldata (alle 32 tabeller,
radantall likt, stikkprøver ok). F2 GRØNN: dialektlag (rebind ?→$n i db.go),
portable upserts/literaler (TRUE/FALSE, ON CONFLICT), Flag-skanner for
boolske kolonner, FTS/vektor dialektdelt (tsvector+pgvector i Postgres,
FTS5+Go-cosine i SQLite), mission-dødkoden fjernet (jf. migrerings-notatet).
Røyk-testet mot Docker-Postgres: alle endepunkter 200, ekte chat- og
dataspørsmål besvart. knowledge-eval mot Postgres: 100 %, p95 185 ms.
Funn underveis (alle fikset): COALESCE(bool, 0)-typefeil, GROUP BY-krav i
unseen-spørringen, COLLATE NOCASE → lower(), og VIKTIGST: SECRET_KEY MÅ
settes i miljøet ved Postgres-drift (nøkkelfila lå ved siden av SQLite-fila;
uten env ville en NY nøkkel gjort alle credentials udekrypterbare — koden
nekter nå eksplisitt). Gjenstår: F4 prod-cut-over på nordavind-1.

## Estimat

F2 er hoveddelen. Ingen frontend-endringer. Én kort, varslet nedetid.
