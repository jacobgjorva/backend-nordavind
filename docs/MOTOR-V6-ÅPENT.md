# Motor v6 — åpne punkter

Skrevet 2026-07-30 etter Scaleway-utfasingen. Prod kjører FORRIGE binær
(med leverandør-forgrening) og er trygg; branchen er ett skritt foran.

## 1. Mistral rate-limiter hardt (viktigst)

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
