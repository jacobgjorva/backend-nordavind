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
