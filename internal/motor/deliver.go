package motor

import (
	"context"
	"regexp"
	"strings"
	"time"
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

// Compressor skriver et svar tettere uten å miste innhold. Brukes av
// lengdegulvet; tom streng betyr at komprimeringen feilet.
type Compressor interface {
	Compress(ctx context.Context, answer string, limit int) string
}

// FormChecker avgjør om en tekst er ødelagt struktur (lekket verktøykall-
// JSON, degenerert output) som aldri skal vises. Dette er en FAKTA-sjekk
// på form (klammer, anførselstegn, bokstavandel) — aldri på innhold, og
// aldri på formulering.
type FormChecker interface {
	IsJunk(text string) bool
}

// Delivery er leveransen med alle sine deler. Alt bortsett fra Out er
// valgfritt: mangler en del, hoppes den over.
type Delivery struct {
	Out      Emitter
	Floors   Floors
	Writer   Writer
	Facts    FactComputer
	Verifier Verifier
	// Form er formgulvet. Legacy har sjekken på alle tre utslippspunktene
	// sine (målt: uten den lakk verktøy-JSON som svar i 1 av 3 kjøringer);
	// v6 manglet den, og et utkast fullt av verktøysyntaks gikk rett til
	// brukeren (målt i prod 2026-07-30).
	Form FormChecker
	// Compress er lengdegulvet. Probet (kjøring 15): median 36 % kortere,
	// null kontrakttap, null diktede tall på åtte ekte lange svar. Utløses
	// KUN på et observerbart faktum (svaret overstiger metodens budsjett
	// med 50 %), og resultatet godtas KUN når koden har verifisert at
	// hvert rene tall og et eventuelt avsluttende spørsmål overlevde —
	// ellers leveres originalen. Gulvet kan altså aldri miste fakta.
	Compress Compressor
	Log      Logger
	// Limit er svarbudsjettet (metodens tak, ellers flytens).
	Limit int
	// EmptyExplain er databasens egen redegjørelse når alt feilet.
	EmptyExplain func() string

	// Reground skriver svaret på nytt UTEN de udekkede tallene (ett kall,
	// api-laget eier upstream). Resultatet godtas kun når koden har
	// re-verifisert det. Valgfri; nil hopper rett til kildefallbacken.
	Reground func(ctx context.Context, turn *Turn, answer string, offenders []string) string

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

	// Formgulvet FØRST: et utkast som er ødelagt struktur er ikke et svar,
	// det er råstoff. Turen har som regel ekte evidens — skriveren får
	// lage svaret av den i stedet (ett kall), og bunnen er ærlig tomhet.
	// Aldri vis brukeren verktøysyntaks.
	if answer != "" && d.Form != nil && d.Form.IsJunk(answer) {
		if d.Log != nil {
			d.Log.Warn("motor v6: utkast forkastet på form", "metode", string(turn.Method),
				"tegn", len([]rune(answer)))
		}
		answer = ""
	}

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

	// Lengdegulvet: stil lar seg ikke styre i prompt (målt — ordgrense-
	// instruks ga median 0,99), så tettheten håndheves her i stedet.
	if d.Compress != nil && d.Limit > 0 {
		if r := []rune(answer); len(r) > d.Limit*3/2 {
			if tight := strings.TrimSpace(d.Compress.Compress(ctx, answer, d.Limit)); tight != "" {
				if compressionKeptFacts(answer, tight) {
					answer = tight
				} else if d.Log != nil {
					d.Log.Warn("motor v6: komprimering forkastet, mistet innhold",
						"metode", string(turn.Method))
				}
			}
		}
	}

	// Tallkontroll. Grunnlaget er alt turen faktisk hentet, det koden
	// regnet, brukerens eget spørsmål OG samtalen — et tall brukeren selv
	// oppga eller fikk i forrige tur er ikke dikting.
	//
	// INVARIANTEN (2026-08-06, erstatter fotnote-gulvet fra 2026-07-30):
	// i en kildekrevende metode leveres udekkede tall ALDRI som fakta.
	// Fotnoten («les som anslag») ble målt i prod: modellen leverte en HEL
	// diktet tabell med bedriftstall, og brukeren fikk den med en merknad
	// nederst. Eskaleringen er kode, aldri skjønn:
	//   null evidens  → den ærlige bunnen — utkastet er ren hukommelse.
	//   evidens       → ETT omforsøk uten de udekkede tallene, re-verifisert
	//                   i kode; holder det ikke, leveres kildefallbacken og
	//                   gulvene viser det kildene faktisk sier.
	// Metoder uten kildedeklarasjon (samtale, ren logikk) er urørt: fail-open.
	if d.Verifier != nil {
		basis := append(append([]string{}, turn.Evidence...), facts, turn.Question, d.Convo)
		if off := d.Verifier.Unsupported(answer, basis); len(off) > 0 {
			if d.Log != nil {
				d.Log.Warn("motor v6: tall uten dekning i verktøydata",
					"avvik", strings.Join(off, ", "), "metode", string(turn.Method))
			}
			if BudgetFor(turn.Method).Searches > 0 && turn.Method != MethodNone {
				if len(turn.Evidence) == 0 {
					if d.Log != nil {
						d.Log.Warn("motor v6: hukommelsessvar holdt tilbake", "metode", string(turn.Method))
					}
					answer = MemoryHeld()
				} else {
					cleaned := ""
					if d.Reground != nil {
						if rw := strings.TrimSpace(d.Reground(ctx, turn, answer, off)); rw != "" &&
							len(d.Verifier.Unsupported(rw, basis)) == 0 {
							cleaned = rw
						}
					}
					if cleaned != "" {
						answer = cleaned
					} else {
						if d.Log != nil {
							d.Log.Warn("motor v6: omforsøk holdt ikke, leverer kildefallback",
								"metode", string(turn.Method))
						}
						answer = CoverageFallback()
					}
				}
			}
		}
	}

	// Tidsgulvet: regnbar alder rettes, foreldede årstall på tidsdeiktiske
	// spørsmål merkes. Etter tallkontrollen — merknader stables nederst.
	if fixed, changed := TimeFloor(turn.Question, answer, time.Now()); changed {
		if d.Log != nil {
			d.Log.Info("motor v6: tidsgulv grep inn", "metode", string(turn.Method))
		}
		answer = fixed
	}

	d.Answer = StripEmphasis(answer)
	DeliverWithFloors(d.Out, d.Floors, turn, answer, d.Limit)
}

// compressionKeptFacts: godta komprimeringen bare når den faktisk er
// kortere, har beholdt HVERT rene tall (som siffersekvens), og ikke har
// mistet et avsluttende spørsmål. Ren kode — komprimeringens sikkerhetsnett
// er deterministisk, aldri en dommer.
func compressionKeptFacts(original, tight string) bool {
	if len([]rune(tight)) >= len([]rune(original)) {
		return false
	}
	tightDigits := digitsOnly(tight)
	for _, tok := range numTokenRe.FindAllString(original, -1) {
		d := digitsOnly(tok)
		if len(d) >= 2 && !strings.Contains(tightDigits, d) {
			return false
		}
	}
	if strings.Contains(original, "?") && !strings.Contains(tight, "?") {
		return false
	}
	return true
}

var numTokenRe = regexp.MustCompile(`\d[\d .,]*\d|\d`)

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
