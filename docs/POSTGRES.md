# Postgres-migreringen — plan (til godkjenning)

26. juli 2026. Motivasjon: kunnskapshentingen laster i dag ALLE embeddings
(4096 dims, ~16 MB JSON per 1000 lapper) inn i Go per melding. Robust løsning
er vektorsøk I databasen: Postgres + pgvector, som også var planen for
Hetzner-driften. Ingenting bygges før planen er godkjent.

## Målbilde

Én Postgres 16 på nordavind-1 med pgvector: alle tabeller + vektorsøk +
fulltekst i samme database. Hentingen blir «SELECT … ORDER BY embedding <=>
$1 LIMIT 30» (HNSW-indeks, ms-svar opp til millioner av rader) + ts_rank for
nøkkelord — RRF-fusjonen i Go består uendret. SQLite beholdes som lesbar
sikkerhetskopi ved cut-over.

## Viktig teknisk premiss: dimensjoner

qwen3-embedding-8b gir 4096 dims — OVER pgvector-indeksens tak (vector: 2000,
halfvec: 4000). Vi må ned i dimensjoner: qwen3-embedding er MRL-trent, så
vektorene kan trunkeres (f.eks. til 1024 eller 2048) + renormaliseres.
**Porten avgjør**: knowledge-eval kjøres med 4096 (fasit) mot 2048 og 1024 —
vi velger største dimensjon som holder 100 % treff, og re-embedder/trunkerer
eksisterende lapper i migreringen. Faller presisjonen: stopp og diskuter.

## Faser (hver med port)

- **F0 — Beslutninger og målinger.** Dimensjonstest (over). Driver: pgx via
  database/sql (stdlib-kompatibel, minst omskriving). Placeholder-flytt
  `?` → `$n` gjøres mekanisk i store-laget.
- **F1 — Skjema.** Nytt skjema i Postgres, 1:1 med dagens MINUS døde felter
  (agents.category og oppdrags-feltene — jf. migrerings-notatet; sjekk
  mission_activity som fortsatt brukes i spinup). FTS5 erstattes av en
  tsvector-kolonne (norsk config) med GIN-indeks; knowledge_notes får
  `embedding vector(N)` med HNSW (cosine).
- **F2 — Store-laget.** Porter 16 filer (~3600 linjer): placeholders,
  `INSERT OR IGNORE/REPLACE` → `ON CONFLICT`, FTS-synk → tsvector-trigger
  eller eksplisitt oppdatering som i dag. `SearchNotesFTS` → ts_rank.
  `AcceptedNotes`-brute-force erstattes av vektor-spørring; grounding/
  expansion-koden er upåvirket (samme interfaces).
- **F3 — Migreringsscript.** Engangs Go-kommando: leser SQLite, skriver
  Postgres (embeddings trunkeres+renormaliseres til valgt N). Idempotent, kan
  kjøres om igjen. Verifiserer radantall per tabell + stikkprøver.
- **F4 — Deploy på nordavind-1.** Postgres 16 + pgvector via apt, kun
  localhost-lytting, egen db-bruker, daglig pg_dump til disk (samme rytme som
  dagens SQLite-backup). Cut-over: stopp tjenesten, kjør migrering, start med
  DATABASE_URL, verifiser, SQLite-fila arkiveres — rollback = start gammel
  binær mot SQLite.

## Porter (alle må være grønne før cut-over)

`go test ./...`, intent-eval (uendret — den er filbasert), knowledge-eval
mot Postgres-backenden med SAMME treffprosent som SQLite-baseline, og en
manuell røyk-test i chat (dataspørsmål, dokumentspørsmål, e-post).

## Estimat

F0-F2 er hoveddelen (store-laget). F3-F4 er små. Ingen frontend-endringer.
Én kort nedetid ved cut-over.
