package motor

import "strings"

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

// maxNotedNumbers: flere enn dette navngis ikke enkeltvis — da er hele
// svaret ukildet, og den generelle merknaden sier det bedre.
const maxNotedNumbers = 3

// CoverageNote er den ærlige merknaden for turer som HAR kilder, men der et
// enkelttall ikke lot seg matche ordrett. Svaret leveres — å felle det ville
// kostet mer enn det redder (målt 2026-08-07: «1700» i et ellers korrekt
// websvar drepte hele svaret).
func CoverageNote(method MethodKey, turn *Turn, offenders []string) string {
	if len(offenders) == 0 {
		return ""
	}
	if BudgetFor(method).Searches == 0 || method == MethodNone {
		return ""
	}
	if len(offenders) > maxNotedNumbers {
		return "Merk: flere av tallene her fant jeg ikke igjen i kildene — les dem som anslag."
	}
	return "Merk: " + humanJoin(offenders) + " fant jeg ikke igjen i kildene — les det som anslag."
}

// humanJoin: «a, b og c».
func humanJoin(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " og " + items[len(items)-1]
}
