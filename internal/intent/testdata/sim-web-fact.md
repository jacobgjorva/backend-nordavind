# Sim v2 — kun web_fact (websøk)

Mål: web_fact skal treffes når spørsmålet gjelder verden utenfor bedriften,
og ALDRI stjele meldinger som gjelder egne tall, rutiner eller AI-forbruk.
Format: `forventet nøkkel — prompt`. Del B/C er hard-negatives.

## A. Skal til web_fact (30)
1. web_fact — hva står euroen i mot kronen nå?
2. web_fact — hva er styringsrenten i dag?
3. web_fact — hvem er statsminister i Sverige?
4. web_fact — hva skjedde på børsen i dag?
5. web_fact — når går fristen for mva-rapportering ut?
6. web_fact — hva koster diesel nå?
7. web_fact — hva er markedsverdien til Equinor?
8. web_fact — hvilke nyheter er det om bygg og anlegg denne uka?
9. web_fact — hva er konsumprisindeksen for juni?
10. web_fact — hvem eier Komplett?
11. web_fact — hva blir været i Trondheim i helgen?
12. web_fact — hva er en holdingstruktur?
13. web_fact — forklar forskjellen på AS og ENK
14. web_fact — hva er siste nytt om kunstig intelligens?
15. web_fact — hvor mange innbyggere har Norge?
16. web_fact — hva ligger gullprisen på?
17. web_fact — når er det ferielov å ta ut restferie innen?
18. web_fact — hva er arbeidsgiveravgiften i 2026?
19. web_fact — hvem vant sykkelrittet i går?
20. web_fact — hva er kursen på DNB-aksjen?
21. web_fact — er det streik i transportsektoren nå?
22. web_fact — hva betyr EBITDA?
23. web_fact — hvilke land er med i EØS?
24. web_fact — sjekk hva konkurrenten Skeidar priser sofaer til
25. web_fact — hva sier finn.no om bruktpriser på varebiler?
26. web_fact — googl hva GDPR krever ved kundedata
27. web_fact — hva er minstelønn for lagerarbeidere?
28. web_fact — finn åpningstidene til tollvesenet
29. web_fact — hva er inflasjonen i eurosonen?
30. web_fact — hvor mye kostet strømmen i natt?

## B. Hard-negatives: egne tall, IKKE web (10)
31. data_question — hva er omsetningen vår hittil i år?
32. data_question — hvor mange ordre kom inn i går?
33. data_question — hva er vår største kunde målt i kroner?
34. data_question — hvor mye har vi solgt av produkt X denne måneden?
35. data_question — hva er snittprisen vi tar for frakt?
36. data_question — hvor mye lager har vi av varenummer 4402?
37. data_question — hvordan var salget vårt i påsken?
38. usage_stats — hva har AI-en kostet oss så langt?
39. show_table — vis siste fakturaer i en tabell
40. data_question — hvilken selger hos oss presterer best?

## C. Hard-negatives: ligner web, men annen flyt (10)
41. create_routine — følg med på oljeprisen og varsle meg daglig — *(rutine, ikke engangs-søk)*
42. create_routine — si fra når dollaren går under 10 kr
43. edit_routine — endre bitcoin-varselet til hvert kvarter
44. free_chat — skriv et sammendrag av denne artikkelen jeg limer inn — *(innhold gitt, ikke søk)*
45. free_chat — hjelp meg å svare på denne e-posten fra Skatteetaten
46. smalltalk — hva kan du egentlig? — *(evne-spørsmål, ikke fakta)*
47. contract_review — hva sier denne avtalen om oppsigelsestid? — *(dokumentet, ikke verden)*
48. data_question — hvordan ligger vi an mot bransjesnittet vi la inn i fjor? — *(vårt tall)*
49. create_widget — lag en graf over oljeprisen siste år — *(widget-flyt eier dette)*
50. free_chat — hva synes du om prisstrategien vår?

## Grenser vi bevisst tester
- verden vs våre tall: «omsetningen» uten «vår» kontra med
- engangs-søk vs rutine: «hva er kursen» kontra «følg med på kursen»
- søk vs limt innhold: spørre om verden kontra behandle tekst brukeren gir
