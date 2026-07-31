# Kunnskapslaget v2 — diagnose og design

2026-07-31. Premiss fra Jacob: motor v6 er FREDET. Kunnskapslaget utvikles
som egen komponent med egen eval-port, og møter motoren i én smal søm.
Ingenting under bygges uten godkjenning.

## Målfunksjonen

Fange intern kunnskap løpende, hente den på RIKTIG tidspunkt (og ellers tie),
og finne mønstre — slik at modellen oppfører seg som en kollega som kjenner
bedriften, ikke en AI med et vedlegg. Målt som: eval-presisjon (riktig
kunnskap PÅ riktig spørsmål, ingenting på feil), injiserte tegn, latens.

## Diagnose: tre lag lever samtidig, ingen gjør hele jobben

1. **Lappe-skuffen** (knowledge_notes, hybrid vektor+BM25+RRF, eval-port med
   29 fasit-caser + 200 støy): teknisk solid henting — men PROD HAR NULL
   NODER OG NULL EMBEDDINGS. Laget er i praksis dødt, og tersklene
   (0.28/0.34/0.55) er målt for qwen3-embedding, ikke mistral-embed.
2. **Node-grafen** (knowledge_nodes/edges + pending-kø): governance-UI.
   Kantene skrives men brukes nesten aldri i henting. Dobbeltlagring mot
   lappene med manuell synk.
3. **Hjernen** (claims med LUKKET predikat-vokabular, inverser, temporalitet,
   proveniens, prosedyrer, 2-hopps traversering): riktig kjerneidé — en
   påstand kan TRAVERSERES («hvilken dag møter jeg sjefen min» krever to
   fakta som aldri sto i samme setning), en lapp kan bare gjenfinnes.
   40 claims i prod. MEN: henting er flat navnematch + «jeg/vi»-heuristikk,
   og ved treff dumpes ALT innen 2 hopp (brainMaxLines) uten relevansutvalg.
   Målt i prod: «Jeg heter Jacob» → 22 påstander injisert, null verdi, og
   forurenset konteksten.

Fundamentene som er RIKTIGE og beholdes: lukket vokabular, temporalitet,
proveniens, prosedyrer som egen form, uttrekks-lakmustesten («ville en
kompetent AI visst dette?»), governance-v2-prinsippene (kildebekreftelse).

Det som IKKE fungerer og fjernes: tre parallelle sannheter, pending-køen
(erstattes av kildebekreftelse), dump-uten-utvalg, ubrukte kanter som egen
lagring, ukalibrerte terskler.

## Design v2

**Sømmen (eneste kobling til motoren, motoren urørt):**
    Context(tenant, bruker, spørsmål, historikk, flyt) → én tekstblokk (kan være tom)
Flyt-gating (G3) står som i dag. Alt innenfor sømmen kan endres fritt.

**D1 — Hjernen blir master.** Claims + prosedyrer er den strukturerte
sannheten. Dokument-biter består som siterbar råtekst KOBLET til noder.
Node-grafen som egen sannhet pensjoneres — admin-grafen blir et VINDU inn
i hjernen (redigering/oppdrag), ikke et eget lager.

**D2 — Relevans-porten: aldri dump.** Seeds som i dag (navn, @, personlig),
men hver kandidat-påstand SCORES mot spørsmålet (embedding på rendret
påstand) og bare de N mest relevante over kalibrert terskel injiseres.
Tomt spørsmål-signal → tom blokk. «Jeg heter Jacob» skal gi NULL linjer —
det er en eval-case, ikke et håp.

**D3 — Kalibrering før terskler.** mistral-embed måles på fasit + støysettet
(cosine-fordeling relatert vs urelatert) FØR noen terskel settes. Åpent
punkt 3 i MOTOR-V6-ÅPENT lukkes samtidig.

**D4 — Governance v2 (avtalt 2026-07-26).** Kildebekreftelse i chatten
(«Skal jeg huske dette?» — ett klikk, aldri autosend), dokumenter rett inn
med proveniens, dublettvakt med målt terskel, bruksbasert falming.
Pending-køen fases ut.

**D5 — Timing-evalen.** Eval-porten utvides med NEGATIVE caser (spørsmål
der riktig svar er å injisere INGENTING) og traverserings-caser («to fakta,
aldri samme setning»). Porten kjøres før/etter hver endring; rød eval
stopper endringen. Samme rituale som intent-evalen.

**D6 — Mønster (fase 2, egen godkjenning).** Når D1-D5 er grønne: en
periodisk mønster-jobb som ser over claims/bruk og FORESLÅR mønstre til
bekreftelse («fakturaspørsmål kommer alltid siste uken i måneden»).
Bygges aldri før hente-fundamentet er målt friskt.

## Byggeplan (hver del måles før neste, samme disiplin som motoren)

1. Eval-porten utvides (D5-casene + claims-fasit) og kjøres som baseline.
2. Kalibrering av mistral-embed (D3) — bare tall, ingen adferdsendring.
3. Relevans-porten (D2) — målt mot baseline: presisjon opp, tegn ned,
   «Jeg heter Jacob» → 0 linjer.
4. Hjernen som master + graf som vindu (D1) — skriveveier, ingen skjemadød.
5. Governance v2 (D4) — frontend-klikket sist, køen fases ut etter.
6. Mønster-fasen (D6) designes separat når 1-5 er grønne.
