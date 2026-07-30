package motor

import (
	"context"
	"strings"
	"testing"
)

// Tankene under er ORDRETT fra ekte kjøringer (loop_v5/, loop_ab/). De er
// testdata i egenskap av å være virkelige modellutdata, ikke et forsøk på
// å teste kvalitet — det gjøres bare mot ekte modell.
const (
	realThoughtR4 = "Du trenger å vite om ScaleAQ er en direkte konkurrent innenfor fôrflåter til " +
		"oppdrettsanlegg, ikke bare generelt i oppdrettsbransjen.  For å svare må jeg først sjekke " +
		"hva ScaleAQ faktisk leverer av produkter og tjenester."
	realThoughtA1 = "Du trenger et vaktplansystem som er skreddersydd for turnus, passer 25 ansatte, " +
		"og helst er norsk både i språk og support. Jeg vet at det finnes flere norske aktører som " +
		"spesialiserer seg på turnusplanlegging, men jeg må sjekke hvilke som passer best."
)

func TestThoughtStepKeepsTheOpeningUnderstanding(t *testing.T) {
	got := ThoughtStep(realThoughtR4)
	if got == "" {
		t.Fatal("en ekte tanke skal bli et steg")
	}
	if len([]rune(got)) > thoughtMaxChars {
		t.Errorf("steget er %d tegn, taket er %d: %q", len([]rune(got)), thoughtMaxChars, got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("steget skal ende på setningsgrense, fikk %q", got)
	}
	// Dobbel mellomrom fra modellen skal være normalisert bort.
	if strings.Contains(got, "  ") {
		t.Errorf("mellomrom ikke normalisert: %q", got)
	}
	// Den bærende presiseringen skal overleve klippet.
	if !strings.Contains(got, "fôrflåter") {
		t.Errorf("steget mistet poenget: %q", got)
	}
}

func TestThoughtStepCutsAtSentenceNotMidWord(t *testing.T) {
	got := ThoughtStep(realThoughtA1)
	if strings.HasSuffix(got, " …") {
		t.Errorf("denne tanken har setningsgrenser og skal ikke ordklippes: %q", got)
	}
	if n := strings.Count(got, "."); n > 2 {
		t.Errorf("maks to setninger, fant %d punktum: %q", n, got)
	}
}

// En lang tanke uten setningsgrenser skal klippes på ordgrense, aldri midt
// i et ord.
func TestThoughtStepWordCutsLongRun(t *testing.T) {
	long := strings.Repeat("segmentanalyse av markedet ", 30)
	got := ThoughtStep(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("manglende klippemarkør: %q", got)
	}
	if strings.Contains(strings.TrimSuffix(got, " …"), "segmentanalys ") {
		t.Error("klippet midt i et ord")
	}
	if len([]rune(got)) > thoughtMaxChars+2 {
		t.Errorf("for langt: %d tegn", len([]rune(got)))
	}
}

// Desimaltall er ikke setningsslutt.
func TestThoughtStepDoesNotSplitOnDecimals(t *testing.T) {
	got := ThoughtStep("Renten ligger på 4.25 prosent nå, men jeg må sjekke om den er endret siden sist.")
	if !strings.Contains(got, "prosent") {
		t.Errorf("klippet på desimaltegnet: %q", got)
	}
}

// Ekko av VÅRE egne instrukser skal aldri nå brukeren. Dette er en sjekk
// mot kjente interne strenger — et faktum, ikke en formuleringsvakt.
func TestThoughtStepDropsEchoesOfOurOwnInstructions(t *testing.T) {
	for _, echo := range []string{
		" METODE: Svaret avhenger av hvem subjektet ER. Arbeidsrekkefølge: profiler først.",
		"Jeg følger SVARKONTRAKT med tre deler slik det står i instruksen min her.",
		"Dette er et arbeidsnotat, ikke svaret, så jeg skriver det som tekst her.",
	} {
		if got := ThoughtStep(echo); got != "" {
			t.Errorf("instruks-ekko lekket til brukeren: %q", got)
		}
	}
	// Hver metodetekst i katalogen skal fanges av vakten hvis modellen
	// gjentar den — ellers er markørlisten utdatert.
	for key, m := range Catalog {
		if got := ThoughtStep(m.Text); got != "" {
			t.Errorf("metodeteksten for %s ville lekket som tanke: %q", key, got)
		}
	}
}

func TestThoughtStepIgnoresNoise(t *testing.T) {
	for _, noise := range []string{"", "   ", "ok", "Søker.", "\n\n"} {
		if got := ThoughtStep(noise); got != "" {
			t.Errorf("støy skal ikke bli et steg: %q → %q", noise, got)
		}
	}
}

// Ankeret er additivt: uten tanke skal det være spørsmålet alene, altså
// nøyaktig dagens rangering.
func TestRelevanceFallsBackToQuestionAlone(t *testing.T) {
	if got := Relevance("hvem konkurrerer vi med?", ""); got != "hvem konkurrerer vi med?" {
		t.Errorf("uten tanke skal ankeret være uendret, fikk %q", got)
	}
	if got := Relevance("", "  "); got != "" {
		t.Errorf("uten noe skal ankeret være tomt, fikk %q", got)
	}
	got := Relevance("hvem konkurrerer vi med?", realThoughtR4)
	if !strings.Contains(got, "hvem konkurrerer vi med?") || !strings.Contains(got, "ScaleAQ") {
		t.Errorf("ankeret skal bære begge deler: %q", got)
	}
}

// Løkka skal sende tanken som arbeidssteg, og ikke sende noe når tanken
// uteblir. Kontrollflyten er uendret i begge tilfeller.
func TestLoopEmitsThoughtAsStep(t *testing.T) {
	e, _, _, out, _ := newRig(
		ModelResponse{Text: realThoughtR4, Calls: []ToolCall{{ID: "1", Name: "web_search"}}},
		ModelResponse{Text: "Ferdig svar."},
	)
	e.Run(context.Background(), payload(), &Turn{Method: MethodRelation})

	if len(out.steps) != 1 {
		t.Fatalf("forventet ett tanke-steg, fikk %d: %v", len(out.steps), out.steps)
	}
	if !strings.Contains(out.steps[0], "fôrflåter") {
		t.Errorf("steget bar ikke tanken: %q", out.steps[0])
	}
}

func TestLoopIsSilentWithoutThought(t *testing.T) {
	e, _, _, out, d := newRig(
		ModelResponse{Text: "", Calls: []ToolCall{{ID: "1", Name: "web_search"}}},
		ModelResponse{Text: "Ferdig svar."},
	)
	turn := &Turn{Method: MethodRelation}
	e.Run(context.Background(), payload(), turn)

	if len(out.steps) != 0 {
		t.Errorf("uten tanke skal motoren være stille, fikk %v", out.steps)
	}
	if len(turn.Thoughts) != 0 {
		t.Error("ingen tanke å registrere")
	}
	if !d.called {
		t.Error("turen skal levere som vanlig uten tanke")
	}
}

// Et tanke-ekko av vår egen instruks skal fanges av vakten OG likevel
// registreres i turen: ankeret er ikke et brukergrensesnitt.
func TestEchoIsHiddenFromUserButLoopContinues(t *testing.T) {
	echo := "Jeg følger SVARKONTRAKT med tre deler slik instruksen sier, og starter nå."
	e, _, _, out, _ := newRig(
		ModelResponse{Text: echo, Calls: []ToolCall{{ID: "1", Name: "web_search"}}},
		ModelResponse{Text: "svar"},
	)
	turn := &Turn{}
	e.Run(context.Background(), payload(), turn)

	if len(out.steps) != 0 {
		t.Errorf("ekkoet skulle vært skjult, fikk %v", out.steps)
	}
	if len(turn.Thoughts) != 1 {
		t.Error("tanken skal fortsatt være registrert i turen")
	}
}
