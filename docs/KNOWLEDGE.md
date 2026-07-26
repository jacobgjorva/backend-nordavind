# Kunnskapslaget — diagnose og forslag til ny tilnærming

26. juli 2026. Nytt blikk på node-treet/intern kunnskap i lys av
intent-arkitekturen. Ingenting under bygges uten godkjenning.

**Målfunksjonen (Jacobs formulering):** AI-en skal hente riktig intern
kontekst raskest mulig, mest mulig presist og med minst mulig tokens.
Enhver endring måles mot disse tre — presisjon (eval-treff), latens (ms) og
injiserte tegn. Derfor bygges retrieval-evalen (G6) FØRST, som baseline.

## Dagens tilstand (lest av koden)

To lagringsmodeller lever side om side:

1. **Grafen** (`knowledge_nodes` + `knowledge_edges`): typede noder
   (term/prosess/regel/entitet) med governance (pending → accepted via
   admin-panelet) og typede relasjoner. Fylles av bakgrunns-uttrekk fra chat
   (`knowledge_extract.go`: markør-gate + billig modell, maks 3 noder,
   pending).
2. **Lappe-skuffen** (`knowledge_notes` + FTS5): flat retrieval-tabell.
   Chat-fakta speiles inn VED godkjenning (SyncFactNote); dokument-biter
   skrives RETT inn som accepted ved opplasting.

Henting (`knowledge.go`): hybrid vektor (cosine, gulv 0.28) + BM25, fusjonert
med RRF, budsjett 4000 tegn / 25 lapper, injisert som system-tillegg.
Kjører PARALLELT med intent-rutingen under 900 ms-budsjettet (fail-open).

## Det som fungerer

Hybrid-hentingen med RRF er solid og målt (se reference-embedding-notatene);
governance-flyten for chat-fakta er god; dokument-opplasting erstatter gamle
versjoner ved samme filnavn; parallell-kjøringen koster null latens.

## Svakhetene

1. **To sannheter.** Grafen og skuffen må holdes i synk manuelt
   (accept/update/delete). Kantene SKRIVES men brukes ALDRI i henting —
   node-treet gjør i praksis ingen jobb i dag.
2. **Relasjoner kobles nesten aldri.** `NodeByTitle` krever eksakt
   tittelmatch; uttrekkets «relations» treffer sjelden noe.
3. **Asymmetrisk governance.** Chat-fakta krever godkjenning; dokument-biter
   går rett til accepted. Et opplastet dokument kan altså lære modellen ting
   ingen har kuratert.
4. **Ingen konflikthåndtering.** To motstridende regler kan begge være
   accepted; ferskhet avgjør kun ved eksakt lik RRF-score. Ingen
   dublett-vakt ut over eksakt tittel.
5. **Flyt-blind injeksjon.** Kunnskap injiseres uansett flyt — også
   smalltalk og panel-flyter som aldri trenger den. Intent-motoren VET
   flyten, men oppslaget bruker den ikke. Ren token-kost.
6. **Skala.** `AcceptedNotes` laster ALLE lapper med embeddings i minnet og
   regner cosine i Go per melding. Fint på dagens volum, dør på store
   tenants med mange dokumenter.
7. **Type-feltet er pynt.** term/prosess/regel/entitet brukes ikke i
   henting, ranking eller presentasjon utover admin-lista.

## Anbefalt retning (samme filosofi som intent-motoren)

Deterministisk der mulig, dommer der usikkert, eval som port:

- **G1 — Én kilde til sannhet: grafen er master.** Lappe-skuffen blir en ren
  projeksjon av accepted noder + dokument-biter (kan beholdes fysisk som i
  dag, men ALL skriving går via noden). Dokument-biter blir noder av typen
  «dokument-bit» under dok-noden.
- **G2 — Kantene får en jobb: 1-hopps utvidelse.** Henting seedes som i dag
  (hybrid + RRF); deretter tas naboene (1 hopp) til topptreffene inn med
  lavere vekt, innenfor samme budsjett. Relasjonskobling fikses samtidig:
  embedding-match (terskel, målt) i stedet for eksakt tittel.
- **G3 — Flyt-bevisst oppslag.** `flows.go` får et Knowledge-felt per rad:
  smalltalk/paneler hopper over oppslag OG injeksjon (sparer tokens og et
  embed-kall); data/e-post/fri-chat beholder dagens oppførsel.
- **G4 — Dokumenter INN i grafen (Jacobs justering).** Ved opplasting kjøres
  uttrekksmotoren over dokumentet: prosedyrer, regler, skills og termer blir
  ordinære noder med kanter til dok-noden — strukturert, koblet og
  kuraterbart som alt annet. Godkjenning skjer på de FÅ uttrukne nodene
  (lesbart for et menneske), ikke per bit. Råbitene beholdes som
  «dokument-bit»-noder under samme dok-node: uttrekk alene mister detaljer,
  og bitene trengs for sitering og detaljspørsmål. Presisjonsgevinst: et
  prosesspørsmål treffer den distillerte prosess-noden (kort, presis, få
  tokens) i stedet for tre rå tekstbiter.
- **G5 — Dublett/konflikt-vakt ved accept.** Ved godkjenning måles embedding-
  likhet mot eksisterende accepted; over terskel → forslag om Å ERSTATTE i
  stedet for å legge til (vist i admin-panelet, aldri auto).
- **G6 — Retrieval-eval som port.** Liten fasit-fil (spørsmål → forventet
  lapp-id) i testdata; kjøres før/etter enhver endring i henting; terskler
  endres kun med grønn eval. Samme rituale som intent-eval.

Skalering (vektorindeks: sqlite-vec/pgvector) utsettes til etter
Postgres-migreringen — ikke en del av dette.

## Alternativer vurdert

- **A: Dropp grafen helt** (kun flat skuff). Enklest, men mister
  admin-grafen, typene og muligheten for utvidelse — og treet er allerede
  bygget og fylt.
- **C: Full graf-RAG** (community-oppsummering, multi-hopp, sentralitet).
  Overkill for dagens volum; G2 gir mesteparten av gevinsten for en brøkdel
  av kompleksiteten.

## Kostnad og migrering

Fase 1 (G1-G4) endrer ingen lagringsskjemaer — kun skriveveier og oppslag.
G3 REDUSERER tokens og embed-kall. G2 koster null ekstra nettverkskall
(kantene ligger i SQLite). G5 er ett embed-kall ved accept (skjer sjelden).
Migrering: engangs-backfill som kobler eksisterende dokument-biter til
dok-noder (dataene finnes allerede i `documents`/`document_chunks`).
