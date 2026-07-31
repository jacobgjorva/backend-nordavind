# Kunnskapslaget — full audit (2026-07-31)

Tre parallelle koderevisjoner (innfesting, governance/frontend, måling) +
datakvalitetsmåling mot prod. Kun funn her — designet står i
KUNNSKAP-V2-DESIGN.md. Fil:linje-detaljene ligger i revisjonsrapportene;
dette er syntesen.

## Hvordan laget sitter i maskineriet i dag

Én inngang: handleChatCompletions. Kunnskapsoppslag kjører PARALLELT med
intent-rutingen under routingBudget (1800 ms, fail-open); injeksjonen skjer
ETTER ruting, gated på flytens Knowledge-felt (G3). Kunnskapsblokken legges
FØRST i systemmeldingen; motorens metodetekst legges SIST. Sømmen mot motor
v6 er BEKREFTET REN: internal/motor importerer ingenting fra kunnskapslaget
og mottar kun ferdig tekst — laget kan bygges om fritt uten å røre motoren.

## Alvorlige funn

1. HENTING BETALES ALLTID: embedding + DB-søk kjører på hver melding, også
   for flyter som aldri injiserer (data_question, smalltalk) og paneler som
   kaster resultatet. Gaten styrer injeksjon, ikke henting.
2. UOPPNÅELIG GREN: med INTENT_ENGINE=on får design-, widget-, connector-
   og agent-setup-forespørsler ALDRI kunnskap (modus-gate hopper over
   blokken, og ikke-on-grenen er død). Rutine-/agent-kjøringer får heller
   aldri kunnskap — de går utenom hele inngangen.
3. HJERNEN ER UMÅLT: eval-porten måler kun lappe-hentingen (knowledgeContext);
   brainContext har aldri vært målt av noe. Evalens -min er default 0 — den
   kan ikke bli rød. Fire fasit-markører lekker inn i støysettet.
4. DATAKVALITET: av 40 prod-claims er ~14 skrot — «jacob er ansvarlig for
   Dagens plan» ×5 (ingen dublettvakt på claims), «company er en kolonne»
   (skjemametadata), «har frist 50 minutter, frekvens: en gang»
   (engangshendelse), «Bona Fide Vines AS er en Belmonte Fine Wine AS»
   (knekt predikat). Valideringen sjekker FORM mot vokabularet, ikke MENING.
5. DUMP UTEN UTVALG: ved entitetstreff injiseres alt innen 2 hopp
   (brainMaxLines=12) uten relevansscoring — målt i prod: «Jeg heter Jacob»
   → 22 påstander, null verdi, forurenset kontekst.
6. DOBBELTUTTREKK: en tur som passerer begge gatene kjører ExtractToBrain
   TO ganger (turslutt-defer + /knowledge/extract) — to modellkall, dobbel
   dublettrisiko.
7. ASYMMETRISK FEILHÅNDTERING: embedding-feil → BM25-fallback, men
   pg-vektorfeil → tom kontekst uten fallback.

## Dødt (skrives/finnes, leses aldri — fjernes i v2)

Pending-maskineriet komplett (9 elementer: ruter, handlere, store-funksjoner,
/kunnskap-panelets kø — køen kan aldri få innhold siden alt skrives accepted);
document_chunks-tabellen; procedures leses aldri (prosedyrer når ALDRI en
prompt — gull som ligger brakk); knowledge_nodes.embedding;
entities.embedding OG embed-kallene som fyller den (betalt nettverk for
ingenting); entities.last_seen; documents.raw_text; notes.chat_id/user_id;
AcceptedNodes/NeighborSummaries/CreateDocument/AcceptedChunks/Procedure.

## Halvdødt

G2-kantutvidelsen virker kun dokument→fakta; fakta→dokument er garantert
null (filter dropper kandidaten). NodeByTitle brukes som id-gjenfinner fordi
ingestFact ikke returnerer id. «Skal jeg huske dette?» er erstattet av
minnekortet (governance v2 delvis LEVERT); tren-tilbudet og dok-uttrekk til
graf virker. Dok-uttrekk kjører på Large 3 tross «billig kall»-kommentarer,
og telles ikke i token-regnskapet (mangler userKey).

## Det som faktisk virker og beholdes

Hybrid-hentingen (vektor+BM25+RRF+relevansfilter) med enslig-ord-vern;
dok→fakta-kantutvidelse; minnekort-flyten; dokumentopplasting med
proposisjonering og ferskhetsregel; dublettvakt på fakta (0.75, auto-erstatt);
graf-editoren med bruksbasert falming; hjernens fundament (lukket vokabular,
inverser, temporalitet, proveniens, supersede); @-nevninger; eval-harnessets
arkitektur (fixtures + støy + tre metrikker).
