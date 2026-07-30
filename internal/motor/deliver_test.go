package motor

import (
	"context"
	"strings"
	"testing"
)

type fakeWriter struct {
	calls  int
	facts  []string
	drafts []string
	out    string
}

func (w *fakeWriter) Write(_ context.Context, _ *Turn, facts, draft string) string {
	w.calls++
	w.facts = append(w.facts, facts)
	w.drafts = append(w.drafts, draft)
	return w.out
}

type fakeFacts struct{ text string }

func (f fakeFacts) Facts(*Turn) string { return f.text }

type fakeVerifier struct {
	unsupported []string
	sawBasis    []string
}

func (v *fakeVerifier) Unsupported(_ string, basis []string) []string {
	v.sawBasis = basis
	return v.unsupported
}

type capturingLog struct{ warns []string }

func (l *capturingLog) Info(string, ...any) {}
func (l *capturingLog) Warn(msg string, args ...any) {
	l.warns = append(l.warns, msg)
}

// Et brukbart utkast uten kode-regnede fakta skal leveres som det er. Ekstra
// skrivekall på et ferdig svar er ren kostnad.
func TestDeliverKeepsGoodDraftWithoutExtraCall(t *testing.T) {
	out, w := &fakeOut{}, &fakeWriter{out: "omskrevet"}
	d := &Delivery{Out: out, Writer: w, Facts: fakeFacts{""}}

	d.Deliver(context.Background(), &Turn{}, "Styringsrenten er 4,25 prosent.")

	if w.calls != 0 {
		t.Errorf("utkastet holdt — ingen omskriving skulle skjedd (%d kall)", w.calls)
	}
	if len(out.content) != 1 || out.content[0] != "Styringsrenten er 4,25 prosent." {
		t.Errorf("feil svar levert: %v", out.content)
	}
}

// Kode har regnet noe modellen ikke så: da MÅ svaret skrives om, ellers
// mangler tallene som er hele poenget.
func TestDeliverRewritesWhenCodeComputedFacts(t *testing.T) {
	out := &fakeOut{}
	w := &fakeWriter{out: "Salget falt 12 prosent, og korrelasjonen er svak (r=-0,12)."}
	facts := "Beregnet i kode: Pearson r=-0,12 over 12 perioder."
	d := &Delivery{Out: out, Writer: w, Facts: fakeFacts{facts}}

	d.Deliver(context.Background(), &Turn{}, "Salget falt.")

	if w.calls != 1 {
		t.Fatalf("forventet nøyaktig ett skrivekall, fikk %d", w.calls)
	}
	if w.facts[0] != facts {
		t.Errorf("skriveren fikk ikke faktaene: %q", w.facts[0])
	}
	if w.drafts[0] != "Salget falt." {
		t.Errorf("skriveren skal se utkastet: %q", w.drafts[0])
	}
	if !strings.Contains(out.content[0], "r=-0,12") {
		t.Errorf("det omskrevne svaret ble ikke levert: %v", out.content)
	}
}

// Tomt utkast (rundetaket nådd) skal skrives fra det som er hentet.
func TestDeliverWritesWhenDraftEmpty(t *testing.T) {
	out, w := &fakeOut{}, &fakeWriter{out: "Her er det jeg fant."}
	d := &Delivery{Out: out, Writer: w, Facts: fakeFacts{""}}

	d.Deliver(context.Background(), &Turn{UsedTool: true}, "")

	if w.calls != 1 {
		t.Errorf("tomt utkast skal gi ett skrivekall, fikk %d", w.calls)
	}
	if out.content[0] != "Her er det jeg fant." {
		t.Errorf("feil svar: %v", out.content)
	}
}

// Feiler skrivingen også, skal brukeren få den ærlige bunnen — aldri
// stillhet og aldri en intern feiltekst.
func TestDeliverFallsBackToHonestEmpty(t *testing.T) {
	out, w := &fakeOut{}, &fakeWriter{out: ""}
	d := &Delivery{Out: out, Writer: w, Facts: fakeFacts{""}}

	d.Deliver(context.Background(), &Turn{UsedTool: true}, "")

	if len(out.content) != 1 || !strings.Contains(out.content[0], "kildene jeg har") {
		t.Errorf("forventet ærlig bunn, fikk %v", out.content)
	}
	if !out.done {
		t.Error("strømmen skal avsluttes")
	}
}

// Databasens egen redegjørelse vinner over den generiske bunnen.
func TestDeliverUsesDatabaseExplanation(t *testing.T) {
	out := &fakeOut{}
	explain := "Spørringen mot Kunder feilet: tabellen finnes ikke."
	d := &Delivery{Out: out, EmptyExplain: func() string { return explain }}

	d.Deliver(context.Background(), &Turn{UsedTool: true}, "")

	if out.content[0] != explain {
		t.Errorf("databasens redegjørelse skulle vært brukt, fikk %q", out.content[0])
	}
}

// Tallkontrollen er TELEMETRI: den logger og slipper svaret gjennom. En
// dommer som kunne felt svaret felte gode svar oftere enn dårlige.
func TestVerifierLogsButNeverBlocks(t *testing.T) {
	out := &fakeOut{}
	v := &fakeVerifier{unsupported: []string{"137,7"}}
	log := &capturingLog{}
	answer := "Boxon omsatte for 137,7 millioner i fjor."
	d := &Delivery{Out: out, Verifier: v, Log: log}

	d.Deliver(context.Background(), &Turn{}, answer)

	if len(out.content) != 1 || out.content[0] != answer {
		t.Errorf("svaret skal leveres uendret tross avvik: %v", out.content)
	}
	if len(log.warns) != 1 {
		t.Errorf("avviket skal logges, fikk %d advarsler", len(log.warns))
	}
}

// Grunnlaget for tallkontrollen må inneholde både evidensen, kode-faktaene
// og brukerens eget spørsmål — et tall brukeren selv oppga er ikke dikting.
func TestVerifierBasisIncludesQuestionAndFacts(t *testing.T) {
	v := &fakeVerifier{}
	d := &Delivery{Out: &fakeOut{}, Verifier: v, Facts: fakeFacts{"Beregnet i kode: 42"}}
	turn := &Turn{Question: "vi omsetter for 40 millioner, hvem ligner?", Evidence: []string{"kilde A"}}

	d.Deliver(context.Background(), turn, "svar")

	joined := strings.Join(v.sawBasis, "|")
	for _, want := range []string{"kilde A", "Beregnet i kode: 42", "40 millioner"} {
		if !strings.Contains(joined, want) {
			t.Errorf("grunnlaget mangler %q: %v", want, v.sawBasis)
		}
	}
}

// Leveransen skal virke uten noen valgfrie deler.
func TestDeliverWorksWithOnlyEmitter(t *testing.T) {
	out := &fakeOut{}
	d := &Delivery{Out: out}
	d.Deliver(context.Background(), &Turn{}, "Et svar.")

	if len(out.content) != 1 || !out.done {
		t.Errorf("minimal leveranse feilet: %v done=%v", out.content, out.done)
	}
}

// Svaret skal strippes for fet skrift FØR det sendes — gulvet gjelder også
// tekst som kommer fra omskrivingen.
func TestDeliverStripsEmphasisFromRewrite(t *testing.T) {
	out := &fakeOut{}
	w := &fakeWriter{out: "Velg **Shyft** til turnusplanlegging."}
	d := &Delivery{Out: out, Writer: w, Facts: fakeFacts{"noe regnet"}}

	d.Deliver(context.Background(), &Turn{}, "utkast")

	if strings.Contains(out.content[0], "**") {
		t.Errorf("fet skrift overlevde leveransen: %q", out.content[0])
	}
}
