# Kunnskapsgraf v2 — målinger

## Kjøring 1 (del 0): kalibrering + baseline, 2026-07-31

Kalibrering av mistral-embed (27 relevante par, 306 urelaterte):

```
RELEVANT   p5=0.771  p50=0.836  p95=0.888
URELATERT  p5=0.665  p50=0.716  p95=0.757
gap: 0.757 → 0.771
```

To konklusjoner, begge viktige:

1. DAGENS TERSKLER ER MENINGSLØSE for mistral-embed: vecFloor 0.28 og
   vecStrong 0.34 ligger LANGT under selv urelatert-fordelingens p5 (0.665)
   — alt passerer alltid. Mistanken fra MOTOR-V6-ÅPENT pkt 3 er bekreftet
   med tall. Datadrevet nivå: gulv ~0.75, sterk ~0.77.
2. GAPET ER SMALT (0.014): en ren absoluttterskel vil alltid være skjør på
   denne modellen. Relevans-porten i del 2 designes derfor RELATIVT: krav om
   både nivå (≥ ~0.75) og MARGIN over spørringens egen støygulv-fordeling,
   ikke én magisk konstant.

Baseline på utvidet fasit (34 caser: 26 gamle + markørlekkasjer tettet +
5 near-miss-negative): 26/34 (76 %), snitt 1034 tegn, p50 206 ms.
ALLE bom er near-miss-negative: «takk for hjelpen med lagertallene» får
2 703 tegn intern kunnskap injisert, «hva koster et kjøleaggregat» 2 663.
Det gamle settet sa 100 % — de nye casene viser at dagens henting ikke kan
tie. Nettopp adferden relevans-porten skal fikse, nå målbar.

## Kjøring 2 (del 2): relevans-porten + scope + kantutvidelse, 2026-07-31

Samme 34-casers fasit, v2-veien (KNOWLEDGE=v2):

```
                      v1 (baseline)   v2
treff                 26/34 (76 %)    33/34 (97 %)
snitt injisert        1 034 tegn      255 tegn
p50                   206 ms          277 ms
```

Portens konstanter (gate.go): gulv 0.765 (mellom urelatert-p95 og
relevant-p5), margin 0.045 mot feltets median (formen skiller «har noe» fra
«har ingenting»), FTS-bonus 0.015 for uavhengig støtte. Gulvet ble justert
én gang (0.750 → 0.765) da porttesten viste at gråsonen slapp gjennom —
dokumentert, målt, ferdig.

Kantutvidelsen aktiveres KUN når porten alt har funnet noe, begge
retninger (fakta→dokument var garantert null i v1), og løftet
kjølevare-casen (4412-koden via kant-naboen).

KJENT GRENSE, bevisst ikke lappet: «kan du oversette til engelsk: vi mottar
varene på rampen» injiserer 363 tegn — teksten LIGNER varemottaksdokumentet
semantisk, og at oppgaven er oversettelse er en oppgavetype-nyanse, ikke et
relevansspørsmål. En ordliste-vakt ville vært nøyaktig lappingen vi ikke
driver med. Tas hvis flere caser i klassen dukker opp.

Lekkasjetesten (to enheter, begge henteveier, privat-scope) er grønn og er
fra nå en del av porten.

## Kjøring 3 (del 3): modellprobe for prosedyre-uttrekk, 2026-07-31

Falsifikasjonskriterier satt FØR kjøring: kandidaten består hvis (a) alle
agn-dokumentets steg fanges i riktig rekkefølge uten diktede steg, og (b)
et rent faktadokument (kontroll) gir null prosedyrer.

```
                         agn (8 steg)        kontroll (fakta)     tid
mistral-small-2603       ✓ alle, ingen dikt  ✓ {"procedures":[]}  0,3-1,8s
mistral-large-2512       ✓ alle, ingen dikt  ✗ DIKTET 3-4 steg    2-4s
```

Large diktet en bookingprosedyre av faktadokumentet i BEGGE
instruks-variantene («ansatt kontakter resepsjonen for å melde interesse»
står ingen steder). Small 4 består begge. VALG: mistral-small-2603 —
målt bedre, 5x raskere, billigere. Instruksen ble strammet én gang
underveis («hvert steg må stå i dokumentet», tom-liste-eksempel) og er
versjonen som gjelder.

BIFANGST: modell-ID-en mistral-small-3.2-24b-instruct-2506 er UTE av
katalogen — v1s brainModel og extractModel pekte på den, så hjerne-uttrekk
og minnekort-destillering har feilet stille i prod. Begge er flyttet til
mistral-small-2603 (katalog-verifisert).

Del 3 for øvrig: dokument-scope arves av alle lapper (default opplasterens
enhet; «hele firmaet» er et bevisst valg), prosedyrer lagres som hentbare
lapper med kant til dok-noden, og lekkasjetesten dekker nå også
kantutvidelsen over enhetsgrenser.
