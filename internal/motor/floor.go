package motor

import "strings"

// Gulvene: det koden garanterer uansett hva modellen fant på.
//
// HVA ET GULV ER. En regel som utløses av et FAKTUM turen etterlot seg —
// hvor mange rader kom tilbake, ble en tabell vist, står tallet i kildene —
// og som koster null tokens. Aldri en regel som leser hvordan svaret er
// formulert: hver eneste formuleringsvakt vi har bygget måtte skrotes, for
// modellen finner alltid en ny vending.
//
// HVORFOR REKKEFØLGEN LIGGER I KODE. Et gulv som appendes etter svaret er
// en leveransekontrakt: tabellen skal komme før observasjonen, observasjonen
// før kildenoten, og strømmen avsluttes til slutt. Legges dette i en prompt,
// er det et ønske. Her er det en garanti.
//
// Selve gulvene bor der de er målt (api-pakken: insight.go, dbstrategy.go,
// narrate.go). Motoren eier bare rekkefølgen og de rene tekstoperasjonene
// som ikke trenger noe fra omverdenen.

// Floors er kontrakten api-laget oppfyller. Hvert kall er valgfritt for
// implementasjonen — returnerer den ingenting, hopper leveransen videre.
type Floors interface {
	// Table viser turens tabell hvis den finnes og ikke alt er vist.
	Table(turn *Turn)
	// Insight er kodens egen observasjon om datagrunnlaget, som én setning.
	Insight(turn *Turn, answer string, limit int)
	// SourceNote navngir kilden når tenanten har flere.
	SourceNote(turn *Turn, answer string)
	// NextStep er ett konkret neste steg, bygget av det turen faktisk gjorde.
	NextStep(turn *Turn, answer string)
}

// StripEmphasis fjerner fet skrift og overskrifter fra svaret.
//
// MÅLT: modellen uthever kandidatnavn med ** uansett hva instruksen sier
// («**Glomma Papp AS** er …»), og legger på ### når svaret blir langt. Huset
// skriver løpende tekst. Dette er en ren tekstoperasjon på et observerbart
// tegn, ikke en tolkning av innhold — derfor er den lovlig som gulv.
//
// Kodeblokker røres ALDRI: der er stjerner og firkanter innhold.
func StripEmphasis(answer string) string {
	var out strings.Builder
	inFence := false
	for i, line := range strings.Split(answer, "\n") {
		if i > 0 {
			out.WriteString("\n")
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out.WriteString(line)
			continue
		}
		if inFence {
			out.WriteString(line)
			continue
		}
		out.WriteString(stripLine(line))
	}
	return strings.TrimSpace(out.String())
}

func stripLine(line string) string {
	// Overskrift → vanlig setning. Blir den stående alene som en etikett
	// («Anbefaling»), er den fortsatt lesbar som en linje.
	trimmed := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trimmed)]
	if strings.HasPrefix(trimmed, "#") {
		trimmed = strings.TrimLeft(trimmed, "#")
		trimmed = strings.TrimSpace(trimmed)
		line = indent + trimmed
	}
	// Fet og kursiv: fjern markørene, behold teksten.
	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "__", "")
	return line
}

// Deliver kjører leveransen: svaret først, deretter gulvene i fast
// rekkefølge, og til slutt slutt-på-strøm.
//
// answer er ferdig tekst. Gulvene får den for å kunne tie når svaret alt
// dekker det de ville sagt — de sammenligner mot FAKTA i teksten (et tall,
// et kolonnenavn), aldri mot formuleringen.
func DeliverWithFloors(out Emitter, floors Floors, turn *Turn, answer string, limit int) {
	answer = StripEmphasis(answer)
	if answer != "" {
		out.Content(answer)
	}
	if floors != nil {
		// Rekkefølgen er kontrakten: tabellen er en del av svaret,
		// observasjonen kommenterer den, kildenoten fotnoter helheten,
		// og neste steg peker videre.
		floors.Table(turn)
		floors.Insight(turn, answer, limit)
		floors.SourceNote(turn, answer)
		floors.NextStep(turn, answer)
	}
	out.Done()
}

// HonestEmpty er svaret når turen ikke ga noe å svare med. Ærlig om hva som
// ble forsøkt, aldri en rådump og aldri en usannhet om databasen.
//
// dbExplain er api-lagets redegjørelse for databaseforsøkene (explainAttempts
// i dbstrategy.go) — tom streng når basen ikke var involvert.
func HonestEmpty(turn *Turn, dbExplain string) string {
	if strings.TrimSpace(dbExplain) != "" {
		return dbExplain
	}
	if turn.UsedTool {
		return "Jeg fant ikke noe som svarer på dette i kildene jeg har. Still gjerne spørsmålet " +
			"litt smalere, så prøver jeg igjen."
	}
	return "Jeg fikk ikke satt sammen et svar på dette nå. Prøv gjerne å formulere det litt annerledes."
}
