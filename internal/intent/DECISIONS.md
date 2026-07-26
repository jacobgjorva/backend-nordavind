# Intent engine — valg, resonnement og beslutninger

Notat fra utviklingen av den initielle motoren (juli 2026). Dette er
begrunnelsene bak arkitekturen slik den står på branch `intent-engine`.
Vedlikeholdsritualet står i `registry.go`; dette notatet forklarer hvorfor
det ser ut som det gjør.

## Utgangspunkt

Målet er sylskarp ruting av chatmeldinger til riktig flyt (widget, rutine,
websøk, datasvar osv.) slik at hver flyt kan få kun sine verktøy, riktig
modell og egne grenser. Kravene vi satte: minst 95 % riktig ruting på
realistiske meldinger, p95 under 250 ms i normal drift, og at feil aldri
blokkerer — alt ukjent skal falle trygt til fri chat som har alle verktøy.

## Arbeidsmåte: eval først, ett verktøy om gangen

Vi jobber ikke bredt. Hver flyt får sitt eget sim-sett med fasit
(`testdata/sim-web-fact.md` var først ut, 50 caser: 30 som skal treffe
web_fact og 20 «feller» som ligner men skal andre steder). Settet lages og
gjennomgås manuelt av Jacob før kjøring, og prompts skal ALDRI overlappe
register-eksemplene — da måler vi gjenkjennelse, ikke generalisering.
Hver reell feilruting blir en varig linje i `testdata/eval.jsonl`, så
settet vokser og fungerer som port for alle senere endringer.

## Funn 1: eksempler alene skalerer ikke

Første kjøring ga 44 %. Å legge til flere web_fact-eksempler i registeret
løftet bare til 58 %. Grunnen: embeddingen måler tekstlikhet, ikke mening —
«arbeidsgiveravgiften i 2026» ligner ikke på noen eksempler, uansett hvor
mange vi legger til. Beslutning: eksempel-jakt per bom er whack-a-mole og
brukes kun som førstelinje-vedlikehold, ikke som løsning på systematiske
klasser av feil.

## Funn 2: dommeren så bare nakne nøkkelnavn

I 14 av 20 gjenværende bom hadde embeddingen faktisk riktig flyt på topp,
men dommeren valgte feil — den fikk kun nøkkelnavn, og «data_question»
lyder riktig for «hva er gullprisen». Beslutning: dommeren får hver
kandidats beskrivelse fra registeret. Løft: 58 → 76 %.

## Funn 3: topp-3-innsnevring og tvangsvalg var fellene

To strukturelle problemer gjensto. (1) Når riktig flyt ikke nådde topp 3 på
tekstlikhet, kunne dommeren aldri redde det. (2) Dommeren var tvunget til å
velge en flyt selv når ingen passet (lim-inn-tekster, meningsspørsmål).
Beslutning — den skalerbare løsningen i stedet for flere lapper:

- Embedding beholdes KUN som hurtigsti for klare treff (direct-terskelen).
- Alt usikkert går til dommeren, som klassifiserer mot HELE det
  rollefiltrerte registeret (alle beskrivelser) pluss et eksplisitt
  `free_chat`-valg («ingen av disse»).
- Token-kostnaden er triviell: ~400–500 input-tokens på MidModel, kun på
  usikre meldinger, 16 tokens ut — hundredeler av én vanlig chatrespons.

Løft: 76 → 92 %.

## Funn 4: multi-snarveien skjulte avgjørbare tilfeller

Nesten-like kandidater gikk tidligere rett til fri chat (MethodMulti).
Men embedding-støy skapte falske «uavgjort» («lag en graf over oljeprisen»:
knowledge_admin 0.632 vs create_widget 0.630 — åpenbart widget). Beslutning:
multi-snarveien fjernes; nesten-like går også til dommeren, som selv velger
free_chat ved ekte sammensatte ønsker. Ekte multi-intent taper ingenting:
utfallet er fri chat i begge design.

## Funn 5: dommeren må dømme handling, ikke tema

Siste bom-klasse var handling-vs-tema: «si fra når dollaren går under
10 kr» handler om valuta (tema: web_fact) men ber om overvåking (handling:
create_routine). Beslutning: dommer-instruksen sier eksplisitt at
handlingen vinner over temaet, med to korte eksempler. Samtidig ble
create_routine-beskrivelsen utvidet til å dekke terskelvarsling, ikke bare
faste intervaller. Resultat: hoved-eval 72/72 (100 %), sim 49–50/50 der
resten var transient API-svikt, ikke feilklassifisering.

## Prinsipper vi holder fast på

- Aldri ny logikk som svar på enkeltbom; logikkendringer kun for
  strukturelle feilklasser, avtalt eksplisitt før bygging.
- Terskler endres kun sammen med grønn eval-kjøring.
- Fail-open overalt: embedding-feil, dommer-feil, timeout → fri chat.
  Transient leverandør-ustabilitet (Scaleway-natten 25.07: 2/50 embed-kall
  feilet, outliere på 6–7 s) skal IKKE utløse logikkendringer.
- Sim-fasit kan være diskuterbar («hva synes du om prisstrategien vår?» —
  free_chat eller data_question); diskuterbare caser avgjøres av Jacob,
  ikke tunes rundt.
- Fri chat er alltid trygt utfall: den har alle verktøy, så en «bom» til
  fri chat koster kvalitet, aldri funksjonalitet.

## Porter (fast regel, avtalt med Jacob 2026-07-26)

Endringer i en flyt slippes ikke uten grønn port for den flyten:

- Intent-ruting: `go run ./cmd/intent-eval` grønn (accuracy ≥ 90 %, p95-krav).
- E-postflyten (mail_search/mail_read/mail_compose): sim-suiten i
  `testdata/sim-email.md` kjøres og skal være grønn (referanse: 93 %).
- Grounding/kildekontroll: enhetstestene i `internal/api/grounding_gate_test.go`
  (kjøres alltid) + grounding-probene i `sim_prompts.jsonl` ved flytendringer.

Flow-sim koster penger per kjøring: kjør porten for flyten som er endret,
ikke hele suiten, og aldri gjentatte runder uten avtale.

## Status og neste steg

Motoren står med: direct-hurtigsti (score ≥ 0.60, margin ≥ 0.08) →
dommer over hele registeret + free_chat for alt annet. Enhetstester grønne.
Neste: re-kjøre eval i rolig time for offisiell grønn kjøring (p95-kravet),
commit på `intent-engine`, deretter neste verktøy-sett i simmen (samme
metode som web_fact). Prod står fortsatt i shadow-modus.
