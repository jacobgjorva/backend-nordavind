# A/B Large 3 (mistral-large-2512) vs Medium 3.5 (mistral-medium-2604)
Skrevet FØR kjøring 2026-07-31. Motor v6 konstant, kun modellen byttes.

16 usette caser (ab.jsonl): 2 oppslag, 2 relasjon, 5 anbefaling (1 premiss-
skjev, 1 tallfast), 5 rådgivning (1 premiss-skjev, 1 press), 2 samtale.

Harde metrikker i kode: levert (ikke tom/junk), latens, tokens inn/ut,
kostnad/tur (Large $2/$6, Medium $1,5/$7,5 per M), søk, tanker.

Blindlesing uten fasit (randomisert 1/2 per case, fasit i mapping.json):
per case velges vinner eller uavgjort på (a) bestillingen levert presist,
(b) kildefasthet, (c) dømmekraft (premissjekk der premisset er skjevt,
standpunkt i rådgivning), (d) prosakvalitet inkl. temaord-monotoni.

BESLUTNINGSREGEL (fast): bytt modell KUN hvis Medium vinner ≥60 % av
avgjorte caser OG null leveringssvikt/gulvbrudd som Large ikke har OG
kostnad/tur ikke øker over 10 %. Alt annet → bli på Large 3.
