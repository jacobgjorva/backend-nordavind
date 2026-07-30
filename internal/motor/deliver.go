package motor

import (
	"context"
	"strings"
)

// Leveransen: fra modellens utkast til det brukeren faktisk får.
//
// HVA SOM SKJER, i rekkefølge:
//
//	1. Har koden regnet noe modellen ikke visste om, eller er utkastet tomt?
//	   → ETT skrivekall. Aldri en kaskade.
//	2. Ingenting å svare med? → den ærlige bunnen (floor.go).
//	3. Tallkontroll som TELEMETRI: avvik logges, svaret vises.
//	4. Gulvene i fast rekkefølge, og slutt på strøm.
//
// HVORFOR TALLKONTROLLEN IKKE BLOKKERER. v1-v4 hadde dommere som kunne felle
// et svar og utløse omforsøk. De felte gode svar oftere enn de reddet
// dårlige: «385,9» av 385 865 969 ble dømt som dikting, brukeren fikk en
// naken tabell, og turen tok 69 sekunder ekstra. Large 3 er målt ærlig, så
// v6 logger avviket og lar svaret gå. Én omskriving skjer bare når
// grunnlaget FAKTISK er utvidet av kode — aldri fordi en dommer var i tvil.

// Writer skriver sluttsvaret når utkastet ikke holder. Ett kall, kompakt
// kontekst: persona + samtale + fakta. Aldri hele historikken på nytt.
type Writer interface {
	Write(ctx context.Context, turn *Turn, facts, draft string) string
}

// FactComputer regner det koden kan regne selv av turens egne data —
// statistikk, korrelasjon. Tom streng når det ikke er noe å regne.
type FactComputer interface {
	Facts(turn *Turn) string
}

// Verifier sjekker svaret mot kildegrunnlaget og returnerer det som ikke
// kan spores. Resultatet er TELEMETRI, ikke en dom.
type Verifier interface {
	Unsupported(answer string, basis []string) []string
}

// Delivery er leveransen med alle sine deler. Alt bortsett fra Out er
// valgfritt: mangler en del, hoppes den over.
type Delivery struct {
	Out      Emitter
	Floors   Floors
	Writer   Writer
	Facts    FactComputer
	Verifier Verifier
	Log      Logger
	// Limit er svarbudsjettet (metodens tak, ellers flytens).
	Limit int
	// EmptyExplain er databasens egen redegjørelse når alt feilet.
	EmptyExplain func() string

	// Convo er samtaleutdraget (siste turer). Det er GYLDIG grunnlag for
	// tallkontrollen: en oppfølging som gjentar forrige turs tall skal
	// aldri merkes som udekket (målt lærdom fra v3 — kildekontrollen
	// felte navn fra historikken som dikting og kostet en 69-sekunders
	// omvei).
	Convo string

	// Answer er det som faktisk ble levert. Hjernen lærer av utvekslingen,
	// og uten dette stopper kunnskapstreet å vokse i nettopp de samtalene
	// som betyr mest.
	Answer string
}

// Deliver leverer turen.
func (d *Delivery) Deliver(ctx context.Context, turn *Turn, draft string) {
	answer := strings.TrimSpace(draft)

	// Kode-regnede fakta: modellen så dem ikke da den skrev, så svaret må
	// skrives om med tallene inkludert. Dette er den ENESTE grunnen til et
	// ekstra skrivekall utover et tomt utkast.
	facts := ""
	if d.Facts != nil {
		facts = strings.TrimSpace(d.Facts.Facts(turn))
	}
	if d.Writer != nil && (answer == "" || facts != "") {
		if written := strings.TrimSpace(d.Writer.Write(ctx, turn, facts, answer)); written != "" {
			answer = written
		}
	}
	if answer == "" {
		explain := ""
		if d.EmptyExplain != nil {
			explain = d.EmptyExplain()
		}
		answer = HonestEmpty(turn, explain)
	}

	// Tallkontroll. Grunnlaget er alt turen faktisk hentet, det koden
	// regnet, brukerens eget spørsmål OG samtalen — et tall brukeren selv
	// oppga eller fikk i forrige tur er ikke dikting.
	//
	// Avvik logges som telemetri, og DEKNINGSGULVET (coverage.go) gjør dem
	// synlige: i en kildekrevende metode får udekkede tall én ærlig
	// merknad i svaret. Det er hele forskjellen på et anslag brukeren kan
	// vurdere og et tall hen tar en beslutning på.
	if d.Verifier != nil {
		basis := append(append([]string{}, turn.Evidence...), facts, turn.Question, d.Convo)
		if off := d.Verifier.Unsupported(answer, basis); len(off) > 0 {
			if d.Log != nil {
				d.Log.Warn("motor v6: tall uten dekning i verktøydata",
					"avvik", strings.Join(off, ", "), "metode", string(turn.Method))
			}
			if note := CoverageNote(turn.Method, turn, off); note != "" {
				answer = strings.TrimRight(answer, " \n") + "\n\n" + note
			}
		}
	}

	d.Answer = StripEmphasis(answer)
	DeliverWithFloors(d.Out, d.Floors, turn, answer, d.Limit)
}
