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
	em.vecs["hei koble"] = []float32{0.7, 0.69, 0}
	j := &stubJudge{pick: "smalltalk"}
	e := buildEngine(t, em, j)
	d := e.Resolve(context.Background(), "hei koble", true)
	if d.Method != MethodJudge || d.Key != "smalltalk" {
		t.Fatalf("ville ha judge smalltalk, fikk %+v", d)
	}
	if len(j.seen) == 0 || len(j.seen) > candN {
		t.Fatalf("dommeren skal få 1-%d kandidater, fikk %v", candN, j.seen)
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
	em.vecs["hei koble"] = []float32{0.7, 0.69, 0}
	e := buildEngine(t, em, &stubJudge{err: errors.New("nede")})
	d := e.Resolve(context.Background(), "hei koble", true)
	// Beste kandidat er over directScore → skal falle tilbake til den.
	if d.Method != MethodDirect || d.Key != "connect_database" {
		t.Fatalf("ville ha direct-fallback, fikk %+v", d)
	}
}

func TestJudgeOutsideEnumRejected(t *testing.T) {
	em := registryStub()
	em.vecs["hei koble"] = []float32{0.7, 0.69, 0}
	e := buildEngine(t, em, &stubJudge{pick: "create_widget"}) // ikke kandidat
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
