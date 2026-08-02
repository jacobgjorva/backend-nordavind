package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Med flagget av skal motoren aldri ta turen — og aldri sende noe.
// Dette er hele sikkerhetsnettet under utrullingen.
func TestMotorIsInertWhenFlagOff(t *testing.T) {
	s := quietServer()
	s.cfg = config.Config{} // ENGINE tom = av

	var sent []string
	answer, took := s.runMotorV6(context.Background(),
		map[string]any{flowKeyField: "research_relation"},
		func(str string) { sent = append(sent, str) }, nil, nil, nil, nil)

	if took {
		t.Error("motoren skal ikke ta turen når ENGINE er av")
	}
	if answer != "" || len(sent) != 0 {
		t.Errorf("ingenting skal være sendt: answer=%q sent=%v", answer, sent)
	}
}

// Utfører-flytene har egne leveransekontrakter og skal falle gjennom til
// legacy selv når motoren er PÅ.
func TestMotorDeclinesExecutorFlowsWhenOn(t *testing.T) {
	s := quietServer()
	s.cfg = config.Config{Engine: "v6"}

	for _, flow := range []string{"create_widget", "export_excel", "email", "create_routine"} {
		var sent []string
		_, took := s.runMotorV6(context.Background(),
			map[string]any{flowKeyField: flow},
			func(str string) { sent = append(sent, str) }, nil, nil, nil, nil)

		if took {
			t.Errorf("%s har egen leveransekontrakt og skal gå til legacy", flow)
		}
		if len(sent) != 0 {
			t.Errorf("%s: ingenting skal være sendt før overlevering: %v", flow, sent)
		}
	}
}

// Et åpent lerret eller en veiviser eier turen selv, uansett flyt.
func TestMotorDeclinesSpecialModes(t *testing.T) {
	s := quietServer()
	s.cfg = config.Config{Engine: "v6"}

	for _, field := range []string{"nordavind_widget", "nordavind_design", "nordavind_agent_setup"} {
		full := map[string]any{flowKeyField: "free_chat", field: true}
		if _, took := s.runMotorV6(context.Background(), full, func(string) {}, nil, nil, nil, nil); took {
			t.Errorf("%s er en spesialmodus og skal eie turen selv", field)
		}
	}
}

// Metodeteksten MÅ nå modellen. Uten denne testen kan v6 se ut til å virke
// — metoden velges, budsjettet brukes, turen går raskere — mens
// fremgangsmåten aldri injiseres. Det skjedde i første A/B-runde: 0 av 7
// svar hadde negativ avgrensning, mens samme motor med teksten ga 3 av 3.
func TestMethodTextReachesTheSystemPrompt(t *testing.T) {
	full := map[string]any{
		flowKeyField: "research_relation",
		"tools":      []any{map[string]any{"type": "function"}},
		"messages": []any{
			map[string]any{"role": "system", "content": "HUSETS REGLER"},
			map[string]any{"role": "user", "content": "hvem konkurrerer vi med?"},
		},
	}

	method := motorMethod(full)
	if extra := motor.BuildSystem("", method, motorHasTools(full)); extra != "" {
		injectSystem(full, extra)
	}

	msgs, _ := full["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)
	content, _ := sys["content"].(string)

	if !strings.Contains(content, "HUSETS REGLER") {
		t.Error("husets regler skal stå igjen")
	}
	if !strings.Contains(content, motor.Catalog[motor.MethodRelation].Text) {
		t.Error("metodeteksten nådde ikke systemprompten")
	}
	if !strings.Contains(content, motor.ThinkRule) {
		t.Error("tenk-regelen nådde ikke systemprompten")
	}
	// Rekkefølgen er målt: tenk-regelen foran metodeteksten.
	if strings.Index(content, motor.ThinkRule) > strings.Index(content, motor.Catalog[motor.MethodRelation].Text) {
		t.Error("tenk-regelen skal stå foran metodeteksten")
	}
}

// Uten verktøy skal tenk-regelen utebli, men metodeteksten stå.
func TestNoThinkRuleWithoutTools(t *testing.T) {
	full := map[string]any{
		flowKeyField: "smalltalk",
		"messages":   []any{map[string]any{"role": "system", "content": "REGLER"}},
	}
	extra := motor.BuildSystem("", motorMethod(full), motorHasTools(full))
	if strings.Contains(extra, motor.ThinkRule) {
		t.Error("uten verktøy skal tenk-regelen utebli")
	}
	if !strings.Contains(extra, motor.Catalog[motor.MethodSmalltalk].Text) {
		t.Error("metodeteksten skal med uansett")
	}
}

// Uten metode skal ingenting injiseres på en verktøyløs tur. free_chat har
// siden 2026-08-02 grunnmetoden (all målt fabrikasjon bodde i den nakne
// løkka), så kontrakten testes nå med en ekte metodeløs flyt.
func TestNoInjectionWithoutMethodOrTools(t *testing.T) {
	full := map[string]any{flowKeyField: "m365_files"}
	if extra := motor.BuildSystem("", motorMethod(full), motorHasTools(full)); extra != "" {
		t.Errorf("uten metode og verktøy skal ingenting injiseres, fikk %q", extra)
	}
}

// Flyten MÅ overleve helt frem til motoren. Slettes den for tidlig, velger
// v6 ingen metode og kjører naken — målt i prod: hver tur logget flyt=""
// selv om rutingen hadde funnet riktig flyt.
func TestFlowSurvivesUntilTheMotorReadsIt(t *testing.T) {
	full := map[string]any{
		flowKeyField: "research_relation",
		flowMaxField: 900,
		"messages":   []any{map[string]any{"role": "user", "content": "hei"}},
	}
	if got := motorFlowKey(full); got != "research_relation" {
		t.Fatalf("motoren måtte se flyten, fikk %q", got)
	}
	if got := motorMethod(full); got != motor.MethodRelation {
		t.Errorf("metoden skulle vært relasjon, fikk %q", got)
	}
	// Først ved upstream-kallet fjernes feltene.
	stripFlowFields(full)
	if _, ok := full[flowKeyField]; ok {
		t.Error("flyt-feltet skal ikke gå upstream")
	}
	if _, ok := full[flowMaxField]; ok {
		t.Error("maks-feltet skal ikke gå upstream")
	}
}

// «reasoning» er et Scaleway-felt Mistral svarer 422 på. Ingen kodesti skal
// sette det (målt i prod: hele turen feilet og brukeren fikk «jeg fikk ikke
// satt sammen et svar»).
func TestNoReasoningFieldAnywhere(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		body := string(raw)
		if strings.Contains(body, `"reasoning":`) {
			t.Errorf("%s setter «reasoning» — Mistral svarer 422 på det", f)
		}
	}
}
