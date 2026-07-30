package motor

import "strings"

// Dekningsgulvet: tall uten kildedekning merkes ærlig i svaret.
//
// MÅLT PROBLEM (prod 2026-07-30): en tur i en kildekrevende metode svarte
// «0,50 til 5 kroner per oppdrag» og «20 og 100 kroner per time» uten ett
// eneste søk. Tallene kom fra hukommelsen og ble presentert som fakta.
// Telemetrien fanget det (avvik="100, 0,50"), men logget bare — brukeren
// så ingenting, og kunne tatt en økonomisk beslutning på anslag.
//
// Årsaken varierer (feilruting, brukt kvote, modellen lot være å søke), og
// å jage årsakene enkeltvis er lappesirkelen som felte v1-v5. Gulvet virker
// på UTFALLET, som er observerbart: metoden deklarerer selv at klassen
// trenger kilder (Budget.Searches > 0), og tall som verken kildene,
// samtalen eller spørsmålet dekker, får én ærlig merknad. Ingen dommer,
// ingen blokkering, ingen omskriving.
//
// AVGRENSNINGER, alle bevisste:
//   - Kun metoder med søkebudsjett. En fri tur (MethodNone) eller samtale
//     har ingen kildedeklarasjon, og «en meter er 100 cm» skal aldri få en
//     merknad. Fail-open.
//   - Kun tall. Navnekontroll uten dommer ga 7 av 10 falske utslag (målt,
//     probelogg kjøring 9) og forblir metodetekstens ansvar.
//   - Samtalen er gyldig grunnlag (målt lærdom fra v3: kildekontrollen
//     felte kundenavn fra historikken som dikting). Kalleren legger derfor
//     samtaleutdraget i basisen FØR kontrollen — en oppfølging som
//     gjentar forrige turs tall skal aldri merkes.

// maxNotedNumbers: flere enn dette navngis ikke enkeltvis — da er hele
// svaret ukildet, og den generelle merknaden sier det bedre.
const maxNotedNumbers = 3

// CoverageNote gir merknaden som skal appendes svaret, eller tom streng.
//
// offenders er tallene tallkontrollen ikke fant dekning for, ferdig
// filtrert (toleranser, årstall, småtall — se motorVerifier i api-laget).
// Gulvet legger ingen egen tolkning oppå: fant kontrollen ingenting, er
// det ingenting å si.
func CoverageNote(method MethodKey, turn *Turn, offenders []string) string {
	if len(offenders) == 0 {
		return ""
	}
	// Kun klasser som selv deklarerer at de bygger på kilder.
	if BudgetFor(method).Searches == 0 || method == MethodNone {
		return ""
	}
	// Ingen kilder i det hele tatt: hele svaret er hukommelse. Si det, i
	// stedet for å ramse opp hvert tall.
	if len(turn.Evidence) == 0 {
		return "Merk: dette svaret bygger på hukommelse, ikke på kilder fra denne turen — sjekk tallene før du bruker dem."
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
