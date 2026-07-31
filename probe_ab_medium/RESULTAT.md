# Resultat: A/B Large 3 vs Medium 3.5 (2026-07-31)

Blindlesing (fasit forseglet til etter scoring, KRITERIER.md skrevet før):

```
                         Large 3    Medium 3.5
Blindseiere               11          2         (3 uavgjort)
Snittlatens/tur           17,3s       7,7s
Kostnad/tur               $0.0179     $0.0115   (-36 %)
Tomme/junk-svar           0           0
```

Beslutningsregelen krevde ≥60 % blindseier for bytte; Medium fikk 15 %.
KONKLUSJON: vi blir på mistral-large-2512.

Mediums målte svakheter i vårt stillas: tanke-lekkasje i svaret
(«Tanken: …» i to rådgivningscaser), spørsmålsvegring i advisory (kastet
spørsmålet tilbake i stedet for standpunkt), tynnere kildebruk i relasjon,
og i CRM-caset diktet Large en pris (2 900-7 000 kr/bruker/mnd) mens
Medium traff (200-1 000) — Mediums ене styrke var tallnøkternhet, pluss
fart og pris.

FELLES SVIKT begge modeller: «fristen i år» ble besvart med 2024-dato
enda prompten sier dagens dato — begge papegøyer kildeårstall over kjent
dato. Kandidat til egen probe: årstall-regel i oppslagsmetoden.
