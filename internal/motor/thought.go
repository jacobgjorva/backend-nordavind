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
// HVORFOR DEN ALDRI KAN ANTAS. Målt over 15 verktøyturer kommer tanken i
// 8 av dem (53 %) — verdifull når den er der, men aldri garantert. Uten
// tanke er motoren stille og turen går nøyaktig som før.
//
// DET DEN IKKE BLE. Designet ville også bruke tanken som siktepunkt for
// kilderangeringen — «spørsmål + tanke» i stedet for spørsmålet alene.
// Målt på fire ekte caser (cmd/v6anchor) endret det 1 av 32 topputdrag.
// Snitt-cosinus steg konsekvent, men rangeringen sorterer bare på
// rekkefølge og har ingen terskel, så en jevn løftning av alle score
// velger nøyaktig samme utdrag. Mekanismen er derfor FJERNET i stedet for
// å stå som ubevist maskineri — det var slik v5 vokste seg umulig.

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
