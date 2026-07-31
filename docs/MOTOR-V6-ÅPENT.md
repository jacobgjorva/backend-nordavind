# Motor v6 — åpne punkter

Skrevet 2026-07-30 etter Scaleway-utfasingen. Prod kjører FORRIGE binær
(med leverandør-forgrening) og er trygg; branchen er ett skritt foran.

## 1. Mistral rate-limiter hardt — NEDGRADERT 2026-07-31

Målt i prod over 24 timer med normal bruk: NULL 429. Kontoen er
pay-as-you-go, og grensene rammer kun eval-harnessenes bursts (sekvensielle
kall med 250ms pause). Dommerfeilene i loggen var gårsdagens 422-er (før
reasoning-fiksen) og to timeouts som fail-safen håndterte. Gjenstår kun:
pacing i harnessene ved behov. Punktet under står som historikk.

Målt: 35 av 147 intent-evalkall får `429` selv MED backoff (400ms → 1200ms
→ 3s), og selv med 250ms pause mellom hvert kall. Uten backoff var det 93.

Konsekvensen i drift er ikke et krasj — det er verre: rutingen faller
stille tilbake på embedding-scoren alene. Dommeren blir aldri spurt, og
ingen ser det. Nøyaktig dette gjorde at v6 kjørte UTEN metode i prod uten
at noe så galt ut.

Nå logges det (`intent: dommer feilet, reddet av score`), men det er
symptomlindring. Reelle veier videre, ikke prøvd:

- Sjekk om nøkkelen er på et gratis/lavt nivå hos Mistral. 429 ved
  sekvensielle kall med 250ms pause er unormalt lavt for en betalt konto.
- Vurder om dommeren skal droppes helt. `direct=82` av 147 klarer seg uten
  den; spørsmålet er om de 65 dommer-kallene er verdt kostnaden og halen.
- Køing/token-bucket foran dommeren, så burst aldri når leverandøren.

## 2. Intent-evalen er RØD på latens

```
Accuracy: 116/127 (91,3 %)   krav ≥ 90 %      ✓
Latens:   p50 530ms, p95 5077ms   krav ≤ 2,5s  ✗
```

p95 er backoff-ventetid, ikke tenketid. I prod kapper `routingBudget`
(1800ms) uansett, så en 5-sekunders retry-kjede rekker aldri brukeren —
den faller fail-open til fri chat.

IKKE senk terskelen for å få grønt. Enten løs rate-limiten (punkt 1), eller
skill målingen: latens UTEN retry er det som beskriver brukeropplevelsen,
og retry-andel er en egen driftsmetrikk.

## 3. Embedding-tersklene er ukalibrerte

`vecFloor = 0.28`, `vecStrong = 0.34`, `relCutoff = 0.55` i `knowledge.go`
er målt for `qwen3-embedding-8b`. Vi kjører nå `mistral-embed`, som har en
annen skala og dimensjon.

Prod har null lagrede embeddings, så ingenting er ødelagt — men tersklene
er gjetning inntil noen måler faktisk cosine på urelatert mot relevant
tekst. Gjør det FØR kunnskapslaget tas i bruk.

## 4. Vision-modellen er uverifisert

`pixtral-large-2411` ga «Invalid model». `pixtral-12b-2409` står i koden nå,
men er ikke bekreftet — videre testing ble stengt av 401 (nøkkelen ser ut
til å være scopet til chat + embeddings).

Bildeopplasting vil feile til dette er avklart mot Mistrals modell-liste.

## 5. Ikke deployet

Branchen har Scaleway-utfasingen; prod har den ikke. Deploy krever at
`/opt/nordavind/env` har `UPSTREAM_BASE_URL=https://api.mistral.ai/v1` og
en Mistral-nøkkel i `UPSTREAM_API_KEY` — ellers dør alle kall, siden
leverandør-forgreningen er borte.

Rekkefølge: sett env på serveren → deploy → verifiser at
«dommer feilet»-linjer IKKE dukker opp i loggen.

## 6. Kildefrie svar i free_chat har intet ærlighetsmerke

Målt i prod 2026-07-30 (logglinjene 21:04-21:05): «ja gjerne» etter et
web_fact-svar falt via sticky til free_chat metode="" — naken løkke. Svaret
var en anbefaling fra hukommelsen («start med Toloka fra Yandex», foreldet
attributt), uten søk, og uten merknad: dekningsgulvet krever både en
kildekrevende metode OG tall, og her fantes ingen av delene.

Viktig presisering fra samme logg: NESTE oppfølging («hva kan jeg forvente
i betaling?») rutet KORREKT til recommendation/anbefaling — verifiser-
regelen var aktiv, modellen lot likevel være å søke, og gulvet merket
svaret. Regeletterlevelse er probabilistisk; ærligheten er gulvet.

Kandidat-løsninger, ingen valgt ennå:
- Rådgivningsmetode på free_chat (planlagt probe) dekker deler av klassen,
  men free_chat er bunnflyten for ALT — stor blastradius, må probes bredt.
- Dekningsgulvets «hukommelse»-variant kunne utvides til kildekrevende
  SPØRSMÅL uten evidens selv uten tall — men «kildekrevende» er semantisk
  når metoden mangler, og det er dommer-territorium. Ikke bygg uten måling.

## 7. Sticky-arv når samtalen bytter oppgaveklasse

Samme logg: sticky er designet for elliptiske oppfølginger («sikker?»), men
en samtale kan BYTTE klasse underveis (faktum → anbefaling). «ja gjerne»
som aksept av et tilbud assistenten selv ga, arver forrige flyt i stedet
for tilbudets klasse. Dokumentert med denne samtalen som belegg; avgjøres
med holdout-måling, ikke fra caset.

## 8. Prosamonotoni: temaordet gjentas

Målt 2026-07-30 på 121 lagrede svar: median toppord-rate 5,2 %, verstinger
9-10 % — alltid temaordet («kontormøbler», «loven», «oppgave»). Forsterkes
når brukerens kriterium ER ordet (metoden begrunner hver kandidat mot
kriteriet). Ikke en motorfeil; modellens norske prosa. En «varier
språket»-regel er umålbar stapling og bygges ikke. Riktig spor hvis dette
skal løftes: persona-/stemmearbeid i egen målt runde, eller sterkere
skrivemodell.
