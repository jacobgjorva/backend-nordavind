package motor

import "strings"

// Tankekanalen: modellens eget arbeidsnotat, gjort om til to ting den
// aldri har vært før — synlig fremdrift for brukeren, og siktepunkt for
// kilderangeringen.
//
// HVORFOR DETTE ER GRATIS. Prosaen kommer i SAMME completion som
// verktøykallene. Vi har alltid betalt for den, og alle tidligere motorer
// kastet den: assistentmeldingen ble skrevet tilbake med content: "".
// v6 bruker den to steder uten et eneste ekstra kall.
//
// HVORFOR DEN ALDRI KAN ANTAS. Målt over flere kjøringer kommer tanken i
// 2 av 7 til 6 av 9 turer — verdifull når den er der (turer med tanke
// bruker halvparten så mange søk), men aldri garantert. Begge bruksmåtene
// er derfor rent additive: uten tanke faller narrasjonen tilbake på
// verktøystegene og ankeret på spørsmålet alene, nøyaktig som i dag.

// KindThink er steg-typen for tanken. Frontend faller tilbake til
// standardikonet for ukjente typer, så den er trygg å innføre alene.
const KindThink = "think"

// thoughtMinChars: kortere enn dette bærer ikke informasjon nok til å
// forstyrre brukeren med.
const thoughtMinChars = 25

// thoughtMaxChars: taket for ett arbeidssteg. Tanken er 2-4 setninger;
// de to første bærer forståelsen av oppgaven, resten er utdyping som
// hører hjemme i svaret.
const thoughtMaxChars = 220

// internalMarkers er strenger som bare kan komme fra VÅRE egne instrukser.
// Dukker en av dem opp i tanken, er den et ekko av systemprompten og
// aldri noe brukeren skal se.
//
// Merk at dette IKKE er en formuleringsvakt: vi leter ikke etter måter å
// si noe på, vi sammenligner mot kjente interne strenger. Det er et
// faktum, og det er den eneste sjekken av dette slaget som er lov.
var internalMarkers = []string{
	"METODE:", "SVARKONTRAKT", "Arbeidsrekkefølge:", "arbeidsnotat, ikke svaret",
}

// ThoughtStep gjør et arbeidsnotat om til ett arbeidssteg, eller til tom
// streng når det ikke er noe å vise.
//
// Alt her er formatering av en visningsstreng — aldri en beslutning om
// hva motoren skal GJØRE. Løkka oppfører seg likt uansett hva tanken sier.
func ThoughtStep(thought string) string {
	t := strings.Join(strings.Fields(thought), " ")
	if len([]rune(t)) < thoughtMinChars {
		return ""
	}
	for _, m := range internalMarkers {
		if strings.Contains(t, m) {
			return ""
		}
	}
	return firstSentences(t, 2, thoughtMaxChars)
}

// firstSentences klipper til de n første setningene, og aldri lenger enn
// maxRunes. Klippet skjer på setningsgrense når det finnes en innenfor
// taket, ellers på ordgrense — en tanke skal aldri vises halvveis inne i
// et ord.
func firstSentences(s string, n, maxRunes int) string {
	r := []rune(s)
	cut, seen := 0, 0
	for i, c := range r {
		if i >= maxRunes {
			break
		}
		if c == '.' || c == '!' || c == '?' {
			// Desimaltall og forkortelser: et punktum midt i et tall er
			// ikke en setningsslutt.
			if i+1 < len(r) && r[i+1] != ' ' {
				continue
			}
			seen++
			cut = i + 1
			if seen >= n {
				return strings.TrimSpace(string(r[:cut]))
			}
		}
	}
	if cut > 0 {
		return strings.TrimSpace(string(r[:cut]))
	}
	if len(r) <= maxRunes {
		return strings.TrimSpace(s)
	}
	// Ingen setningsgrense innenfor taket: klipp på siste ordgrense.
	head := string(r[:maxRunes])
	if sp := strings.LastIndex(head, " "); sp > maxRunes/2 {
		head = head[:sp]
	}
	return strings.TrimSpace(head) + " …"
}

// Relevance er siktepunktet kildeutdragene rangeres mot: brukerens
// spørsmål pluss det modellen SIST sa den lette etter.
//
// Dette er den generelle mekanismen som erstatter både profil-regexen og
// scope-parameteren tidligere runder foreslo: ingen trigger, ingen
// skjemafelt, bare gjenbruk av en tanke vi allerede har betalt for.
// Eldre tanker er utdatert hensikt og brukes aldri.
func Relevance(question, thought string) string {
	q := strings.TrimSpace(question)
	th := strings.TrimSpace(thought)
	switch {
	case th == "":
		return q
	case q == "":
		return th
	}
	return q + "\n" + th
}
