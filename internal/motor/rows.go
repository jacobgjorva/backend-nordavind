package motor

import (
	"fmt"
	"strings"
)

// Radgulvet: koden aggregerer store resultatsett FØR modellen ser dem.
//
// MÅLT PROBLEM: modellen er svak på lange radlister. Den fant ikke linje
// 317 av 400, og resonnerte seg bort i listen i stedet for å lese den.
// Løsningen er ikke en bedre instruks — det er å ikke sende radene.
//
// Ren funksjon på []string: ingen avhengigheter, lett å teste, og den kan
// aldri utløses av annet enn et FAKTUM (antall rader).

// MaxRawRows er hvor mange rader modellen får se rått.
const MaxRawRows = 20

// SummarizeRows erstatter et stort resultatsett med det koden kan si sikkert
// om det: størrelse, form, og første og siste rad. Modellen får tallene,
// ikke listen.
//
// Bevisst beskjeden: den regner ikke statistikk den ikke kan garantere
// betydningen av. Kolonne-statistikk hører hjemme i faktalaget (del 6),
// der typene er kjent.
func SummarizeRows(cols []string, rows [][]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d rader (for mange til å vises rått). Kolonner: %s.\n",
		len(rows), strings.Join(cols, ", "))
	if len(rows) > 0 {
		fmt.Fprintf(&b, "Første rad: %s\n", strings.Join(rows[0], " | "))
		fmt.Fprintf(&b, "Siste rad: %s\n", strings.Join(rows[len(rows)-1], " | "))
	}
	b.WriteString("Hele tabellen er tilgjengelig for brukeren med show_table.")
	return b.String()
}
