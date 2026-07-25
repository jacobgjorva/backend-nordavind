package intent

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// stubEmbedder gir forhåndsbestemte vektorer per tekst; ukjente tekster får
// nullvektor. Lar oss teste margin-/fallback-logikken uten nettverk.
type stubEmbedder struct {
	vecs map[string][]float32
	err  error
}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := s.vecs[t]; ok {
			out[i] = v
		} else {
			out[i] = []float32{0, 0, 1}
		}
	}
	return out, nil
}

type stubJudge struct {
	pick string
	err  error
	seen []string
}

func (s *stubJudge) Pick(_ context.Context, _ string, keys []string) (string, error) {
	s.seen = keys
	if s.err != nil {
		return "", s.err
	}
	return s.pick, nil
}

// buildEngine lager en motor der alle registertekster får styrte vektorer:
// to «akser» — connect_database nær [1,0,0], smalltalk nær [0,1,0].
func buildEngine(t *testing.T, em Embedder, j Judge) *Engine {
	t.Helper()
	e, err := New(context.Background(), em, j, slog.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func registryStub() *stubEmbedder {
	vecs := map[string][]float32{}
	_, texts := RegistryTexts()
	keys, _ := RegistryTexts()
	for i, txt := range texts {
		switch keys[i] {
		case "connect_database":
			vecs[txt] = []float32{1, 0, 0}
		case "smalltalk":
			vecs[txt] = []float32{0, 1, 0}
		default:
			vecs[txt] = []float32{0, 0, 1}
		}
	}
	return &stubEmbedder{vecs: vecs}
}

func TestDirectWinner(t *testing.T) {
	em := registryStub()
	em.vecs["koble til basen"] = []float32{1, 0.05, 0}
	e := buildEngine(t, em, &stubJudge{})
	d := e.Resolve(context.Background(), "koble til basen", true)
	if d.Method != MethodDirect || d.Key != "connect_database" {
		t.Fatalf("ville ha direct connect_database, fikk %+v", d)
	}
}

func TestJudgeOnTie(t *testing.T) {
	em := registryStub()
	// Nesten likt mellom to akser → margin for liten → dommer.
	em.vecs["hei koble"] = []float32{0.9, 0.85, 0.2}
	j := &stubJudge{pick: "smalltalk"}
	e := buildEngine(t, em, j)
	d := e.Resolve(context.Background(), "hei koble", true)
	if d.Method != MethodJudge || d.Key != "smalltalk" {
		t.Fatalf("ville ha judge smalltalk, fikk %+v", d)
	}
	// Dommeren skal få hele det rollefiltrerte registeret + fri chat + usikker.
	if len(j.seen) != len(Registry)+1 || j.seen[len(j.seen)-1] != AskKey {
		t.Fatalf("dommeren skal få hele registeret + %s + %s, fikk %v", FreeChatKey, AskKey, j.seen)
	}
}

func TestJudgePicksFreeChat(t *testing.T) {
	em := registryStub()
	em.vecs["hei koble"] = []float32{0.9, 0.85, 0.2}
	e := buildEngine(t, em, &stubJudge{pick: FreeChatKey})
	d := e.Resolve(context.Background(), "hei koble", true)
	if d.Method != MethodJudge || d.Key != "" {
		t.Fatalf("dommer-valgt fri chat skal gi tom nøkkel, fikk %+v", d)
	}
}

func TestFloorGivesFreeChat(t *testing.T) {
	em := registryStub()
	// Ortogonal mot alt → under gulvet → fri chat.
	em.vecs["helt urelatert"] = []float32{-1, -1, -1}
	e := buildEngine(t, em, &stubJudge{})
	d := e.Resolve(context.Background(), "helt urelatert", true)
	if d.Method != MethodNone || d.Key != "" {
		t.Fatalf("ville ha none, fikk %+v", d)
	}
}

func TestEmbedderFailureFailsOpen(t *testing.T) {
	e := buildEngine(t, registryStub(), &stubJudge{})
	e.embedder = &stubEmbedder{err: errors.New("nede")}
	d := e.Resolve(context.Background(), "koble til basen", true)
	if d.Method != MethodNone {
		t.Fatalf("embedding-feil skal gi fri chat, fikk %+v", d)
	}
}

func TestJudgeFailureFallsBack(t *testing.T) {
	em := registryStub()
	em.vecs["hei koble"] = []float32{0.9, 0.85, 0.2}
	e := buildEngine(t, em, &stubJudge{err: errors.New("nede")})
	d := e.Resolve(context.Background(), "hei koble", true)
	// Beste kandidat er over directScore → skal falle tilbake til den.
	if d.Method != MethodDirect || d.Key != "connect_database" {
		t.Fatalf("ville ha direct-fallback, fikk %+v", d)
	}
}

func TestJudgeOutsideEnumRejected(t *testing.T) {
	em := registryStub()
	em.vecs["hei koble"] = []float32{0.9, 0.85, 0.2}
	e := buildEngine(t, em, &stubJudge{pick: "finnes_ikke"}) // utenfor enum
	d := e.Resolve(context.Background(), "hei koble", true)
	if d.Method != MethodDirect || d.Key != "connect_database" {
		t.Fatalf("svar utenfor enum skal avvises med fallback, fikk %+v", d)
	}
}

func TestAdminOnlyHiddenForMembers(t *testing.T) {
	em := registryStub()
	em.vecs["koble til basen"] = []float32{1, 0.05, 0}
	e := buildEngine(t, em, &stubJudge{})
	d := e.Resolve(context.Background(), "koble til basen", false)
	if d.Key == "connect_database" {
		t.Fatalf("AdminOnly-flyt skal være usynlig for member, fikk %+v", d)
	}
}

func TestEmptyAndHugeMessages(t *testing.T) {
	e := buildEngine(t, registryStub(), &stubJudge{})
	for _, msg := range []string{"", "   ", string(make([]byte, 3000))} {
		if d := e.Resolve(context.Background(), msg, true); d.Method != MethodNone {
			t.Fatalf("grensetilfelle %q skal gi none, fikk %+v", msg[:min(10, len(msg))], d)
		}
	}
}

func TestRegistrySanity(t *testing.T) {
	seen := map[string]bool{}
	for _, in := range Registry {
		if in.Key == "" || in.Description == "" {
			t.Fatalf("intent uten nøkkel/beskrivelse: %+v", in)
		}
		if seen[in.Key] {
			t.Fatalf("duplikat nøkkel: %s", in.Key)
		}
		seen[in.Key] = true
		if len(in.Examples) < 3 {
			t.Fatalf("%s: minst 3 eksempler kreves, har %d", in.Key, len(in.Examples))
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestNearTieGoesToJudge(t *testing.T) {
	em := registryStub()
	// Sterk på BÅDE connect_database og smalltalk, nesten likt → dommeren
	// avgjør (og kan velge fri chat ved ekte sammensatte ønsker).
	em.vecs["koble til basen og si hei"] = []float32{0.71, 0.7, 0}
	e := buildEngine(t, em, &stubJudge{pick: FreeChatKey})
	d := e.Resolve(context.Background(), "koble til basen og si hei", true)
	if d.Method != MethodJudge || d.Key != "" {
		t.Fatalf("ville ha judge → fri chat, fikk %+v", d)
	}
}

func TestFlowTableCoversRegistry(t *testing.T) {
	for _, in := range Registry {
		f, ok := Flows[in.Key]
		if !ok {
			t.Fatalf("intent %s mangler flyt-rad", in.Key)
		}
		if in.Key == FreeChatKey {
			continue // fri chat er selv bunnen — egen fallback-regel
		}
		if !f.Deterministic {
			if f.Model == "" {
				t.Fatalf("%s: modellflyt uten Model", in.Key)
			}
			if f.MaxChars <= 0 {
				t.Fatalf("%s: modellflyt uten MaxChars", in.Key)
			}
		}
		if f.Fallback != FreeChatKey {
			t.Fatalf("%s: fallback skal være %s i v1", in.Key, FreeChatKey)
		}
	}
	free, ok := Flows[FreeChatKey]
	if !ok || free.Fallback != "" || free.Deterministic {
		t.Fatalf("fri chat-raden er feil: %+v", free)
	}
}

func TestFlowForFallsBackToFreeChat(t *testing.T) {
	if k, _ := FlowFor(Decision{Method: MethodMulti}); k != FreeChatKey {
		t.Fatalf("multi skal gi fri chat, fikk %s", k)
	}
	if k, _ := FlowFor(Decision{Key: "finnes_ikke"}); k != FreeChatKey {
		t.Fatalf("ukjent nøkkel skal gi fri chat, fikk %s", k)
	}
	if k, _ := FlowFor(Decision{Key: "data_question", Method: MethodDirect}); k != "data_question" {
		t.Fatalf("ville ha data_question, fikk %s", k)
	}
}

func TestAskLabelsCoverRegistry(t *testing.T) {
	for _, in := range Registry {
		if AskLabels[in.Key] == "" {
			t.Errorf("intent %s mangler AskLabel", in.Key)
		}
	}
}
