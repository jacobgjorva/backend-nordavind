package api

import (
	"context"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
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
