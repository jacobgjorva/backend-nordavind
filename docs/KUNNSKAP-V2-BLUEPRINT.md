# Kunnskapsgraf v2 — design fra scratch

2026-07-31, branch kunnskap-v2 (isolert; motor v6 fredet). Erstatter forrige
blueprint etter Jacobs verdi/kompleksitet-kutt: PASSIV CHAT-FANGST ER UTE.
Auditens funn (KUNNSKAP-V2-AUDIT.md) står som begrunnelse; v1-koden fases ut,
v2 bygges som ny pakke.

## Verdifokus: hva grafen ER

Tre innholdstyper, ikke flere:

1. DOKUMENTER brukeren bevisst laster opp: proposisjonerte biter for
   gjenfinning + uttrukne PROSEDYRER/SKILLS som strukturerte noder
   («hvordan krediterer jeg kunden» → deres steg, ikke generiske råd).
2. ORGANISASJONEN: ansatte, roller, enheter, ansvar — lagt inn STRUKTURERT
   i onboarding/admin, aldri gjettet fra chat. Gir kollega-svarene («Kari
   er økonomiansvarlig og godkjenner rabatter over 10 %») OG er ryggraden
   for tilgangsstyring.
3. MINNEKORT-FAKTA (eneste chat-vei, beholdt fra forrige beslutning):
   brukeren klikker «husk» på en melding — bevisst handling, klar
   proveniens, null autofangst.

Alt annet fra v1 fases ut: turslutt-læring, /knowledge/extract,
pending-maskineriet, døde tabeller og write-only-felt (auditens liste).

## Data governance: org-modellen er nøkkelen

Scope-modell håndhevet i SPØRRINGEN (aldri i prompten):

    tenant   hele virksomheten (default for småbedrift uten enheter)
    enhet    datterselskap/avdeling — selskap A ser aldri selskap B
    rolle    f.eks. kun økonomi-rollen
    privat   kun brukeren selv

Det smarte grepet: scope ARVES fra konteksten i stedet for å kreve valg —
et dokument får opplasterens enhet som default (kan heves til tenant eller
snevres ved opplasting), org-data er tenant per natur, minnekort-fakta får
delt/privat-valg i kortet. Ingen egen governance-admin: org-strukturen FRA
onboardingen definerer enhetene og rollene, så tilgangsstyringen finnes i
det øyeblikket organisasjonen er registrert.

## Henting: relevans-port, aldri dump (uendret fra forrige design)

Context(ctx, tenant, bruker, spørsmål, historikk, flyt) → Block — eneste søm
mot resten av systemet, motoren mottar kun ferdig tekst. Innenfor: seeds
(navn/@/personlig) + hybrid vektor/BM25, hver kandidat scoret mot spørsmålet
med KALIBRERT terskel (mistral-embed måles først), budsjettert topp-N, og
TOM blokk er et gyldig og vanlig svar. Prosedyrer hentes som prosedyrer.
Henting kjører kun for flyter med Knowledge-flagget, og scope-filteret
ligger i SQL-en.

## Metodikk (samme som motoren)

Kontrakter øverst i pakken (internal/knowledge), ingen nettverkskode i
kjernen; justering som data; uttrekkskvalitet probes mot EKTE modell med
falsifikasjonskriterier før konklusjon; eval-porten bygges FØRST, har
obligatorisk rød-terskel, og kjøres i deploy-flyten. Negative caser
(«injiser ingenting») og traverseringscaser er del av fasiten fra dag én.

## Plan (hver del måles før neste, ingenting bygges uten godkjent del)

    0. Eval + kalibrering      fasit for dok/org/prosedyre-henting + negative;
                               mistral-embed-terskler måles; baseline mot v1
    1. Org-modellen            enheter/roller/ansatte i skjema + API —
                               scope-fundamentet, brukes av alt etter
    2. Pakken                  internal/knowledge: Context()-sømmen med
                               relevans-port og scope-filter
    3. Dokument-inngest v2     opplasting med scope-arv, proposisjonering,
                               prosedyre/skill-uttrekk på liten modell (probet)
    4. Onboarding + admin      org-registrering og graf-editor mot v2
    5. Utfasing av v1          passiv fangst, pending-maskineri, døde tabeller
                               og gamle henteveier slettes; minnekortet kobles
                               til v2-ingest med scope-valg
    6. Mønster-fasen           eget design + egen godkjenning, bygges på målt
                               fundament
