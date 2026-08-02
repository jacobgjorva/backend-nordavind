package api

import (
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/intent"
	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Metoden avledes av flyten. Det er hele koblingen — ingen egen tilstand
// som kan komme i utakt med rutingen.
func TestMethodComesFromRoutedFlow(t *testing.T) {
	cases := map[string]motor.MethodKey{
		"research_relation": motor.MethodRelation,
		"recommendation":    motor.MethodAdvice,
		"web_fact":          motor.MethodLookup,
		"data_question":     motor.MethodAnalysis,
		"smalltalk":         motor.MethodSmalltalk,
		"m365_files":        motor.MethodNone,
		"free_chat":         motor.MethodGround,
		"uklart":            motor.MethodGround,
		"":                  motor.MethodNone,
	}
	for flow, want := range cases {
		full := map[string]any{}
		if flow != "" {
			full[flowKeyField] = flow
		}
		if got := motorMethod(full); got != want {
			t.Errorf("flyt %q ga metode %q, forventet %q", flow, got, want)
		}
	}
}

// Sticky arves gratis: applyIntent setter forrige flyt på payloaden, og
// metoden følger med. Testen fester at det ikke finnes en egen kanal.
func TestStickyFlowCarriesMethodWithoutExtraState(t *testing.T) {
	// Første tur: brukeren spør om konkurrenter.
	full := map[string]any{flowKeyField: "research_relation"}
	if motorMethod(full) != motor.MethodRelation {
		t.Fatal("første tur skal ha relasjonsmetoden")
	}
	// Oppfølging: ruteren har satt SAMME flyt (sticky), og metoden følger.
	followUp := map[string]any{flowKeyField: "research_relation"}
	if motorMethod(followUp) != motor.MethodRelation {
		t.Error("sticky oppfølging skal arve metoden via flyten")
	}
	// Ingen metode-nøkkel i payloaden: metoden er avledet, aldri lagret.
	for k := range full {
		if k != flowKeyField {
			t.Errorf("uventet felt i payload: %q — metoden skal ikke lagres", k)
		}
	}
}

// Hver flyt v6 gir en metode til, må være sticky i flyt-tabellen. Uten det
// mister en oppfølging metoden midt i en samtale.
func TestMethodFlowsAreStickyInFlowTable(t *testing.T) {
	for _, flow := range []string{"research_relation", "recommendation", "data_question"} {
		f, ok := intent.Flows[flow]
		if !ok {
			t.Errorf("flyt %q finnes ikke i tabellen", flow)
			continue
		}
		if !f.Sticky {
			t.Errorf("flyt %q gir en metode, men er ikke sticky — oppfølginger mister den", flow)
		}
	}
}

// Flytene som gir en metode må faktisk ha verktøyene metoden forutsetter.
// En metode som ber om kildelesing i en flyt uten websøk er en stille feil.
func TestMethodFlowsCarryTheToolsTheMethodAssumes(t *testing.T) {
	needsWeb := map[string]bool{
		"research_relation": true, "recommendation": true, "web_fact": true,
	}
	for flow := range needsWeb {
		f := intent.Flows[flow]
		var hasSearch bool
		for _, tool := range f.Tools {
			if tool == intent.ToolWebSearch {
				hasSearch = true
			}
		}
		if !hasSearch {
			t.Errorf("flyt %q har en metode som krever websøk, men verktøyet mangler", flow)
		}
	}
	// Samtale skal IKKE ha verktøy: strukturen bærer oppførselen.
	if len(intent.Flows["smalltalk"].Tools) != 0 {
		t.Error("smalltalk skal stå uten verktøy")
	}
}

// Porten og metoden må være enige: en flyt v6 ikke tar, skal heller ikke ha
// en metode som forventer å kjøre der.
func TestExecutorFlowsHaveNoMethod(t *testing.T) {
	for _, flow := range []string{"create_widget", "export_excel", "email", "create_routine"} {
		if motor.Takes(map[string]any{}, flow) {
			t.Errorf("%s skal ikke tas av motoren", flow)
		}
		if got := motor.For(flow); got != motor.MethodNone {
			t.Errorf("%s eies av legacy, men har metode %q", flow, got)
		}
	}
}
