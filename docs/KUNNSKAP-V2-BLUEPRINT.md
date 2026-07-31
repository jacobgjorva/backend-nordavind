# Kunnskapslaget v2 — blueprint og overordnet plan

2026-07-31, branch kunnskap-v2. Bygger på KUNNSKAP-V2-AUDIT.md (funnene) og
KUNNSKAP-V2-DESIGN.md (retningen). Motor v6 er FREDET — dette dokumentet
endrer ingenting i internal/motor, og sømmen er bekreftet ren i auditen.

## Ambisjonen

En kollega som kjenner bedriften: fanger intern kunnskap løpende, bruker den
på riktig tidspunkt (og tier ellers), kobler fakta som aldri sto i samme
setning, og ser mønstre. Ikke et vedlegg av lapper.

## Blueprintet

### Én pakke, én søm

`internal/knowledge` blir en egen pakke etter motor-oppskriften: kontrakter
øverst, ingen nettverkskode i kjernen, api-laget eier adapterne.

    Context(ctx, tenant, bruker, spørsmål, historikk, flyt) → Block

Block er én tekstblokk (kan være tom) + metadata for logging (kilder, score,
linjetall). handleChatCompletions kaller Context; motoren mottar som før kun
ferdig tekst i systemmeldingen. Alt innenfor pakken kan endres fritt.

### Lagring: hjernen er master, to former for innhold

- STRUKTURERT: entiteter, claims (lukket vokabular, temporalitet, proveniens,
  supersede) og prosedyrer — som i dag, men prosedyrene FÅR EN LESER:
  prosesspørsmål henter prosedyren, ikke bare den avledede ansvars-claimen.
- RÅTEKST: dokument-biter (proposisjonert, embeddet) koblet til dok-node med
  kanter — som i dag. Fakta-lapper består som projeksjon av bekreftede noder.
- FJERNES: hele pending-maskineriet, document_chunks, alle write-only-felt
  og døde funksjoner fra auditens liste. Node-grafen består som VINDU
  (editor), aldri som egen sannhet.

### Henting: relevans-port, aldri dump

1. Seeds som i dag (navn, @, personlig-heuristikk) + hybrid vektor/BM25 for
   lapper — det målte fundamentet beholdes.
2. NYTT: hver kandidat — claim (rendret som setning), prosedyre, lapp —
   scores mot spørsmålet med kalibrert embedding-terskel; kun topp-N over
   terskel injiseres, budsjettert. Ingen treff → TOM blokk. «Jeg heter
   Jacob» → 0 linjer er en eval-case, ikke et håp.
3. Kantutvidelsen fikses begge veier (fakta→dokument er i dag garantert null).
4. Henting skjer KUN når flytens Knowledge-felt sier ja — i dag betales
   embedding på hver melding og kastes.

### Tre innløp, én graf (Jacobs presisering 2026-07-31)

Kunnskap kommer inn tre veier med ulik terskel, men lander i SAMME hjerne
med samme vokabular og proveniens:

1. PASSIV (chat): småfakta som dukker opp naturlig — terskler, faste
   avtaler, preferanser. Minnekort + turslutt-læring, med vaktene under.
   Aldri org-struktur eller prosedyrer herfra.
2. EKSPLISITT (dokumenter): prosedyrer, skills og rutiner LASTES OPP
   bevisst; uttrekket strukturerer dem til prosedyre-noder med dokumentet
   som proveniens. Dette er hovedveien for «hvordan gjør jeg X»-kunnskapen.
3. STRUKTURERT (onboarding/admin): organisasjonen — ansatte, roller,
   enheter, ansvar — legges inn direkte i onboardingen (skjema/graf-editor
   eller import), aldri gjettet fra samtaler. Org-strukturen er samtidig
   fundamentet scope-styringen henger på: enhet og rolle må FINNES før
   kunnskap kan scopes til dem.

### Skriving: én vei, vakter ved døren

Alle fem innganger (turslutt-læring, minnekort, bekreft, dok-uttrekk,
graf-editor) går gjennom ÉN ingest-funksjon med tre vakter: (1) dublettvakt
på claims (i dag finnes den kun på fakta-lapper — «Dagens plan» ×5 må være
umulig), (2) engangshendelse-vakt (kvalifikatorer som «frekvens: en gang»
avvises — varig kunnskap eller ingenting), (3) meningsvakt for miskastede
predikater (skjemametadata som «company er en kolonne» stoppes av samme
lakmustest som chat-uttrekket alt har i prompten, men håndhevet i KODE der
det er avgjørbart). Dobbeltuttrekket per tur fjernes.

### Innfesting i maskineriet (endres i api-laget, ikke motoren)

- Uoppnåelig-gren-buggen fikses: design/widget/connector/setup og
  rutine-kjøringer får et bevisst JA/NEI på kunnskap, ikke et utilsiktet
  aldri.
- pg-vektorfeil får samme BM25-fallback som embedding-feil.
- Dok-uttrekk flyttes til liten modell og telles i token-regnskapet.

### Måling: porten bygges FØRST og kan bli rød

- Kalibrering av mistral-embed på fixtures+støy før noen terskel settes.
- Claims-fasit (gjenfinning OG traversering: «to fakta, aldri samme
  setning»), prosedyre-fasit, negative near-miss-caser (interne ord, null
  relevans — dagens negative er Australia-lette), og presisjonsstraff for
  overinjeksjon. Markørlekkasjen i støysettet tettes.
- -min blir obligatorisk (rød eval stopper endring), og evalen kjøres i
  deploy-flyten.

### Scope og korreksjon (Jacobs krav 2026-07-31)

- SYNLIGHET: hver kunnskapsbit får et scope satt ved skriving og håndhevet
  ved henting — tenant (alle), enhet (datterselskap/avdeling), rolle, eller
  privat (kun brukeren). I dag finnes ingen slik dimensjon: regnskapssjefens
  fakta serveres selgeren, og søsterselskap ser hverandre. Driftsresultat og
  strategi for selskap A skal være USYNLIG for selskap B — håndhevet i
  spørringen, aldri i prompten.
- PERSONLIG BRUK: fakta der subjektet er brukeren selv og ikke virksomheten
  («datteren min heter…», «jeg skal til tannlegen») droppes eller
  privat-scopes automatisk — aldri delt kunnskap.
- KORREKSJON: «nei, det stemmer ikke» i chat blir en førsteklasses vei som
  supersederer claimen med proveniens (hvem korrigerte, når). Supersede
  finnes alt for en-verdi-predikater; nytt er konfliktVAKTEN: motstridende
  claims fra ULIKE kilder flagges i graf-editoren, og hentingen foretrekker
  ferskeste med sterkest proveniens til konflikten er avklart.

### Mønster (fase 2, eget design + godkjenning)

Når fundamentet er grønt: periodisk jobb som ser over claims/bruk og
FORESLÅR mønstre til bekreftelse. Bygges aldri på umålt henting.

## Overordnet plan (hver del måles før neste)

    0. Eval-utvidelse + kalibrering    baseline-tall, ingen adferdsendring
    1. Rydding                         alt dødt ut; testfestet null adferdsendring
    2. Pakke-uttrekk                   internal/knowledge med Context()-sømmen,
                                       dagens henting flyttet UENDRET inn
    3. Relevans-porten                 claims/prosedyrer scores mot spørsmålet;
                                       «Jeg heter Jacob» → 0 linjer måles
    4. Skrivevakter + scope            én ingest, tre vakter + scope-felt og
                                       konfliktvakt; dobbeluttrekk vekk;
                                       prod-claims ryddes (14 skrot-rader)
    4b. Onboarding-inngangen           strukturert org-registrering (ansatte,
                                       roller, enheter) — designes med frontend
    5. Innfestings-fikser              hentegating, uoppnåelig gren, pg-fallback,
                                       liten modell på dok-uttrekk
    6. Mønster-fasen                   eget design, egen godkjenning

Rekkefølgen er valgt så måleporten eksisterer FØR noe endres (0), ingen ny
funksjon bygges på død kode (1-2 før 3-4), og hvert steg kan deployes alene.
