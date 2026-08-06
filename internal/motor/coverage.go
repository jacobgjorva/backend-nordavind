package motor

// Dekningsgulvet, historikk og nåværende form.
//
// V1 (2026-07-30): tall uten kildedekning fikk en «les som anslag»-fotnote.
// Beslutningen var bevisst («ingen dommer, ingen blokkering») etter at
// v1-v4-dommerne felte gode svar. MEN: målt i prod 2026-08-06 leverte
// modellen en HEL diktet tabell med bedriftstall, og fotnoten gjorde den
// bare mer troverdig. Handover-notatet hadde alt utpekt «honest-floor table
// dump» som toppsaken.
//
// V2 (2026-08-06): fotnoten er erstattet av invarianten i Deliver — udekkede
// tall i kildekrevende metoder leveres aldri som fakta. Eskaleringen står i
// deliver.go; her ligger bare den kodebygde fallback-teksten som brukes når
// omforsøket heller ikke lot seg belegge. Gulvene (floor.go) viser turens
// faktiske kildetabell rett under, så brukeren får det kildene sier — aldri
// det modellen husket.

// CoverageFallback er svaret når prosaen ikke lot seg belegge: ærlig om
// hva som skjedde, og peker på kildene gulvene viser. Drive-stilen gjelder
// også her — ett konkret tilbud, aldri en naken tabell uten retning.
func CoverageFallback() string {
	return "Noen av tallene i utkastet mitt lot seg ikke spore til kildene, så jeg holder dem " +
		"tilbake og viser det kildene faktisk sier. Vil du at jeg går dypere i tallene?"
}

// MemoryHeld er svaret når HELE utkastet var hukommelse (null evidens i en
// kildekrevende metode) og heller ikke den tvungne verktøyrunden ga kilder.
// Ærlig om at tall finnes men holdes tilbake — aldri «prøv å omformulere».
func MemoryHeld() string {
	return "Jeg har ikke fått hentet disse tallene fra noen kilde denne turen, og jeg gjetter " +
		"ikke på bedriftstall. Vil du at jeg prøver et konkret oppslag?"
}
