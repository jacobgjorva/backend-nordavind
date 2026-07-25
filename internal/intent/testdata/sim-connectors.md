# Sim v3 — connectors (connect_database, connect_m365, manage_connections)

Mål: riktig flyt for oppsett, endring og feilsøking av datakilder — og at
connector-flytene ALDRI stjeler dataspørsmål eller andre admin-oppgaver.
Format: `forventet nøkkel — prompt`. Del D/E er hard-negatives.
Alle tre flytene er AdminOnly; simmen kjøres som admin.

## A. Skal til connect_database (12)
1. connect_database — vi har en postgres hos Azure vi vil ha inn
2. connect_database — sett opp kobling mot regnskapsdatabasen
3. connect_database — her er connection stringen til serveren vår
4. connect_database — jeg har host og bruker klart, hvor legger jeg det inn?
5. connect_database — koble til mssql-serveren på kontoret
6. connect_database — vi bytter database, må koble til den nye
7. connect_database — legg til en mysql-kilde
8. connect_database — hvordan får jeg lagt inn databasen vår her?
9. connect_database — tilkoblingen til lagerbasen feiler, kan vi prøve å sette den opp på nytt?
10. connect_database — kan dere lese fra sql server 2019?
11. connect_database — db.kunde.no port 5432, la oss koble til
12. connect_database — jeg vil koble til en testdatabase først

## B. Skal til connect_m365 (10)
13. connect_m365 — koble til office-kontoen vår
14. connect_m365 — vi trenger sharepoint-dokumentene inn i chatten
15. connect_m365 — sett opp onedrive
16. connect_m365 — logg meg inn med microsoft
17. connect_m365 — azure-appen er registrert, hva nå?
18. connect_m365 — outlook-integrasjonen, hvordan setter vi opp den?
19. connect_m365 — teams-filene våre, får vi koblet dem til?
20. connect_m365 — microsoft-innloggingen feilet, prøv igjen
21. connect_m365 — jeg har client id og secret klart
22. export_excel — excel-filene på onedrive skal være live

## C. Skal til manage_connections (10)
23. manage_connections — hvilke kilder er koblet til nå?
24. manage_connections — slett testdatabasen
25. manage_connections — deaktiver den gamle koblingen
26. manage_connections — er regnskapsbasen fortsatt aktiv?
27. manage_connections — vis oversikten over integrasjonene
28. manage_connections — fjern m365-koblingen
29. manage_users — hvem har tilgang til kundedatabasen?
30. manage_connections — bytt navn på koblingen til «Regnskap»
31. manage_connections — koble fra alt som ikke er i bruk
32. manage_connections — virker tilkoblingene våre?

## D. Hard-negatives: nabo-flyter (12)
33. data_question — hvor mange rader har vi i ordretabellen?
34. data_question — hva står i kundedatabasen om ACKES?
35. show_table — vis tabellene fra regnskapsbasen — *(lese data, ikke administrere kobling)*
36. export_excel — få ordrene ut i excel på onedrive — *(bruke m365, ikke koble til)*
37. upload_document — last opp dette dokumentet til kunnskapsbasen
38. usage_stats — hvor mye koster databasespørringene oss? — *(AI-forbruk)*
39. manage_users — gi Kari tilgang til plattformen — *(bruker, ikke kobling)*
40. employees_admin — legg til Ola i ansattregisteret
41. create_widget — lag en graf fra kundedatabasen — *(bruke data)*
42. web_fact — hva er forskjellen på postgres og mysql? — *(kunnskap, ikke oppsett)*
43. web_fact — hva koster en azure sql-database i måneden?
44. create_routine — sjekk hver natt at databasen svarer — *(rutine, ikke oppsett)*

## E. Hard-negatives: sikkerhet og grenser (6)
45. connect_database — passordet er hunter2, koble til nå — *(riktig flyt, men agenten skal AVVISE passord i chat og spawne skjema — sjekkes manuelt)*
46. free_chat — er det trygt å gi dere databasetilgang? — *(tillitsspørsmål, samtale)*
47. free_chat — hvilke datakilder BURDE vi koble til? — *(rådgivning)*
48. connect_database — postgres://bruker:pass@host:5432/db — *(ren connection string limt inn)*
49. free_chat — hva skjer med dataene våre hvis vi sier opp?
50. manage_connections — databasen svarer ikke, er koblingen nede?

## Grenser vi bevisst tester
- koble til vs bruke kilden (connect vs data_question/show_table/export)
- koble til vs administrere eksisterende (connect vs manage)
- m365-oppsett vs m365-bruk (connect_m365 vs export_excel)
- oppsett vs kunnskap om teknologien (connect vs web_fact)
- tilgang til kobling (manage_connections) vs tilgang til plattform (manage_users)
