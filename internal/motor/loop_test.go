package motor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Testene her kjører løkkas KONTROLLFLYT med falske kontrakter. Det er
// deterministisk kode — rundetak, kvotestopp, overlevering — og ikke
// modellkvalitet. Kvalitet måles kun mot ekte modell (v6probe); et mocket
// modellsvar er aldri bevis for at et svar er godt.

type fakeModel struct {
	responses []ModelResponse
	err       error
	calls     int
}

func (f *fakeModel) Call(_ context.Context, _ ModelRequest) (ModelResponse, error) {
	f.calls++
	if f.err != nil {
		return ModelResponse{}, f.err
	}
	if len(f.responses) == 0 {
		return ModelResponse{Text: "tomt"}, nil
	}
	r := f.responses[0]
	if len(f.responses) > 1 {
		f.responses = f.responses[1:]
	}
	return r, nil
}

// fakeTools speiler den ekte kjørerens kontrakt: den håndhever budsjettet
// og svarer med kvotetekst i stedet for å utføre kallet.
type fakeTools struct {
	handoffOn string
	ran       []string
}

func (f *fakeTools) Run(_ context.Context, c ToolCall, b Budget, turn *Turn) ToolResult {
	f.ran = append(f.ran, c.Name)
	if c.Name == f.handoffOn {
		return ToolResult{Handoff: true}
	}
	kind := KindOther
	switch c.Name {
	case "web_search":
		kind = KindSearch
	case "fetch_url":
		kind = KindFetch
	}
	if !b.Allow(kind, turn.Spent(kind)) {
		return ToolResult{Text: quotaText, Kind: KindOther}
	}
	return ToolResult{Text: "resultat fra " + c.Name, Kind: kind, Evidence: true}
}

const quotaText = "Kvoten er brukt opp."

type fakeOut struct {
	content []string
	steps   []string
	sources int
	done    bool
}

func (f *fakeOut) Content(s string)     { f.content = append(f.content, s) }
func (f *fakeOut) Step(s, _ string)     { f.steps = append(f.steps, s) }
func (f *fakeOut) Sources(src []Source) { f.sources += len(src) }
func (f *fakeOut) Done()                { f.done = true }

type fakeDeliver struct {
	called bool
	draft  string
}

func (f *fakeDeliver) Deliver(_ context.Context, _ *Turn, draft string) {
	f.called, f.draft = true, draft
}

func newRig(resp ...ModelResponse) (*Engine, *fakeModel, *fakeTools, *fakeOut, *fakeDeliver) {
	m := &fakeModel{responses: resp}
	tl := &fakeTools{}
	out := &fakeOut{}
	d := &fakeDeliver{}
	return &Engine{Model: m, Tools: tl, Out: out, Deliver: d}, m, tl, out, d
}

func payload() map[string]any {
	return map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hei"}}}
}

// Enkleste tur: modellen svarer med tekst i første runde. Ett kall, ingen
// verktøy, leveransen får utkastet.
func TestSingleRoundAnswerCostsOneCall(t *testing.T) {
	e, m, _, _, d := newRig(ModelResponse{Text: "Svaret er 4,25 prosent."})
	turn := &Turn{Method: MethodLookup}

	if handoff := e.Run(context.Background(), payload(), turn); handoff {
		t.Fatal("et vanlig svar skal ikke gi overlevering")
	}
	if m.calls != 1 {
		t.Errorf("forventet 1 modellkall, fikk %d", m.calls)
	}
	if !d.called || d.draft != "Svaret er 4,25 prosent." {
		t.Errorf("leveransen fikk ikke utkastet: %q", d.draft)
	}
	if turn.UsedTool {
		t.Error("ingen verktøy ble brukt")
	}
}

// Arbeidsturen: kall verktøy, så svar. Tanken skal fanges, resultatene skal
// tilbake i samtalen, og leveransen få sluttsvaret.
func TestToolRoundThenAnswer(t *testing.T) {
	e, m, tl, out, d := newRig(
		ModelResponse{Text: "trenger segmentet først", Calls: []ToolCall{{ID: "1", Name: "web_search", Args: `{"query":"x"}`}}},
		ModelResponse{Text: "Ferdig svar."},
	)
	turn := &Turn{Method: MethodRelation}
	p := payload()

	e.Run(context.Background(), p, turn)

	if m.calls != 2 {
		t.Errorf("forventet 2 modellkall, fikk %d", m.calls)
	}
	if len(tl.ran) != 1 || tl.ran[0] != "web_search" {
		t.Errorf("verktøy ikke kjørt: %v", tl.ran)
	}
	if len(turn.Thoughts) != 1 || turn.Thoughts[0] != "trenger segmentet først" {
		t.Errorf("tanken ble ikke fanget: %v", turn.Thoughts)
	}
	if turn.Searches != 1 || !turn.UsedTool {
		t.Errorf("forbruk ikke ført: søk=%d brukt=%v", turn.Searches, turn.UsedTool)
	}
	// samtalen skal ha vokst med assistentkall + tool-svar
	msgs, _ := p["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("forventet 3 meldinger (bruker, assistent, tool), fikk %d", len(msgs))
	}
	assistant, _ := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" || assistant["content"] != "trenger segmentet først" {
		t.Errorf("assistentmeldingen bærer ikke tanken: %+v", assistant)
	}
	tool, _ := msgs[2].(map[string]any)
	if tool["role"] != "tool" || !strings.Contains(tool["content"].(string), "web_search") {
		t.Errorf("verktøysvaret kom ikke tilbake: %+v", tool)
	}
	if !d.called || d.draft != "Ferdig svar." {
		t.Error("leveransen fikk ikke sluttsvaret")
	}
	if out.done {
		t.Error("løkka avslutter ikke strømmen selv — det eier leveransen")
	}
}

// Rundetaket er metodens, ikke en global konstant. Nås det, leveres det som
// FINNES — aldri en tom tur og aldri en intern feiltekst.
func TestRoundCeilingComesFromMethodAndStillDelivers(t *testing.T) {
	loop := ModelResponse{Text: "leter videre", Calls: []ToolCall{{ID: "1", Name: "web_search"}}}
	e, m, _, _, d := newRig(loop) // samme respons hver gang: modellen gir seg aldri
	turn := &Turn{Method: MethodLookup}

	e.Run(context.Background(), payload(), turn)

	want := Catalog[MethodLookup].Budget.Rounds
	if m.calls != want {
		t.Errorf("oppslag har rundetak %d, fikk %d kall", want, m.calls)
	}
	if turn.Rounds != want {
		t.Errorf("turen teller %d runder, forventet %d", turn.Rounds, want)
	}
	if !d.called || d.draft != "" {
		t.Error("ved rundetak skal leveransen kalles med tomt utkast, ikke hoppes over")
	}
}

// Budsjettet er per metode. Oppslag har ett søk; det andre skal møte den
// ærlige kvotemeldingen i stedet for å bli utført.
func TestQuotaStopsSecondSearchForLookup(t *testing.T) {
	two := ModelResponse{Text: "søker", Calls: []ToolCall{
		{ID: "1", Name: "web_search"}, {ID: "2", Name: "web_search"},
	}}
	e, _, _, _, _ := newRig(two, ModelResponse{Text: "ferdig"})
	turn := &Turn{Method: MethodLookup}
	p := payload()

	e.Run(context.Background(), p, turn)

	if turn.Searches != 1 {
		t.Errorf("kun ett søk skal telle innenfor oppslagsbudsjettet, fikk %d", turn.Searches)
	}
	msgs, _ := p["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if !strings.Contains(last["content"].(string), "Kvoten") {
		t.Errorf("andre søk skal få kvotemeldingen, fikk %q", last["content"])
	}
}

// Ukjent verktøy = hel overlevering. Ingenting skal være sendt til
// brukeren, ellers får hen halve svar fra to motorer.
func TestUnknownToolHandsOffWithoutEmitting(t *testing.T) {
	e, _, tl, out, d := newRig(ModelResponse{
		Text: "skriver e-post", Calls: []ToolCall{{ID: "1", Name: "mail_compose"}},
	})
	tl.handoffOn = "mail_compose"
	turn := &Turn{}

	if handoff := e.Run(context.Background(), payload(), turn); !handoff {
		t.Fatal("ukjent verktøy skal gi overlevering")
	}
	if !turn.Handoff {
		t.Error("turen skal være merket som overlevert")
	}
	if d.called {
		t.Error("leveransen skal ikke kjøre på en overlevert tur")
	}
	if len(out.content) > 0 || out.done {
		t.Error("ingenting skal være sendt til brukeren før overlevering")
	}
}

// Upstream nede i FØRSTE runde: ingenting er levert, så turen er trygg å gi
// videre til legacy i stedet for å vise en feil.
func TestUpstreamFailureFirstRoundHandsOff(t *testing.T) {
	e, m, _, out, d := newRig()
	m.err = errors.New("nede")

	if handoff := e.Run(context.Background(), payload(), &Turn{}); !handoff {
		t.Fatal("feil i første runde skal gi overlevering")
	}
	if len(out.content) > 0 || d.called {
		t.Error("ingenting skal være sendt eller levert")
	}
}

// Upstream nede SENERE: vi har alt vist arbeid, så turen svarer ærlig her.
func TestUpstreamFailureLaterAnswersHonestly(t *testing.T) {
	m := &fakeModel{responses: []ModelResponse{
		{Text: "søker", Calls: []ToolCall{{ID: "1", Name: "web_search"}}},
	}}
	out := &fakeOut{}
	d := &fakeDeliver{}
	// Første runde går gjennom; deretter er upstream nede.
	e := &Engine{Model: &failAfter{inner: m, after: 1}, Tools: &fakeTools{}, Out: out, Deliver: d}

	if handoff := e.Run(context.Background(), payload(), &Turn{}); handoff {
		t.Fatal("etter levert arbeid skal turen ikke overleveres")
	}
	if len(out.content) != 1 || !strings.Contains(out.content[0], "kontakt med modellen") {
		t.Errorf("forventet ærlig nede-melding, fikk %v", out.content)
	}
	if !out.done {
		t.Error("strømmen skal avsluttes")
	}
	if d.called {
		t.Error("leveransen skal ikke kalles når upstream er nede")
	}
}

type failAfter struct {
	inner *fakeModel
	after int
	n     int
}

func (f *failAfter) Call(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	f.n++
	if f.n > f.after {
		return ModelResponse{}, errors.New("nede")
	}
	return f.inner.Call(ctx, req)
}

// Tokenforbruket summeres over runder — grunnlaget for kostnadsmålingen i
// A/B-en. Utledes aldri; kommer fra leverandøren.
func TestUsageAccumulates(t *testing.T) {
	e, _, _, _, _ := newRig(
		ModelResponse{Text: "t", Calls: []ToolCall{{ID: "1", Name: "web_search"}},
			Usage: Usage{PromptTokens: 100, CompletionTokens: 10}},
		ModelResponse{Text: "svar", Usage: Usage{PromptTokens: 200, CompletionTokens: 20}},
	)
	turn := &Turn{}
	e.Run(context.Background(), payload(), turn)

	if turn.Usage.PromptTokens != 300 || turn.Usage.CompletionTokens != 30 {
		t.Errorf("forbruk feilsummert: %+v", turn.Usage)
	}
}

// Systemprompten: kjerne + ÉN metode + tenk-regel, i den rekkefølgen.
func TestBuildSystemComposesOneMethodAndThinkRule(t *testing.T) {
	got := BuildSystem("KJERNE", MethodRelation, true)
	if !strings.HasPrefix(got, "KJERNE") {
		t.Error("husets regler skal stå først")
	}
	if !strings.Contains(got, Catalog[MethodRelation].Text) {
		t.Error("metodeteksten mangler")
	}
	// Rekkefølgen er MÅLT: tenk-regelen må stå FORAN metodeteksten, ellers
	// dør tankekanalen (0 av 5 ekte kjøringer mot 6 av 9).
	if strings.Index(got, ThinkRule) > strings.Index(got, Catalog[MethodRelation].Text) {
		t.Error("tenk-regelen skal stå foran metodeteksten")
	}
	if !strings.HasSuffix(got, Catalog[MethodRelation].Text) {
		t.Error("metodeteksten er turens mest spesifikke instruks og skal stå sist")
	}
	// Ingen andre metodetekster får sive inn.
	for key, m := range Catalog {
		if key == MethodRelation && strings.Contains(got, m.Text) {
			continue
		}
		if key != MethodRelation && strings.Contains(got, m.Text) {
			t.Errorf("systemprompten bærer også %q — stabling er forbudt", key)
		}
	}
}

// Uten verktøy gir tenk-regelen ingen mening: den ville bedt om et
// arbeidsnotat til en jobb modellen ikke kan utføre.
func TestBuildSystemSkipsThinkRuleWithoutTools(t *testing.T) {
	got := BuildSystem("KJERNE", MethodSmalltalk, false)
	if strings.Contains(got, ThinkRule) {
		t.Error("tenk-regelen skal utebli når turen ikke har verktøy")
	}
	if !strings.Contains(got, Catalog[MethodSmalltalk].Text) {
		t.Error("metodeteksten skal fortsatt med")
	}
}

// Naken løkke (ingen metode) skal gi nøyaktig husets regler + tenk-regel.
func TestBuildSystemWithoutMethodAddsNothingExtra(t *testing.T) {
	got := BuildSystem("KJERNE", MethodNone, true)
	if got != "KJERNE"+ThinkRule {
		t.Errorf("uten metode skal ingenting annet legges til, fikk %q", got)
	}
}

// Nil-logger er lovlig: motoren skal aldri krasje på manglende logg.
func TestNilLoggerIsSafe(t *testing.T) {
	e, _, _, _, _ := newRig(ModelResponse{Text: "svar"})
	e.Log = nil
	e.Run(context.Background(), payload(), &Turn{})
}
