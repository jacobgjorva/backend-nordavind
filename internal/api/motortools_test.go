package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// quietServer er en Server med stille logg — resten av kodebasen antar at
// log alltid er satt, så testene gjør det samme i stedet for å innføre
// nil-vakter som ikke finnes noe annet sted.
func quietServer() *Server {
	return &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Kvoten skal utløse på et FAKTUM (telleren), og et kall uten råd skal
// aldri nå verktøykoden. Testen bruker et verktøy uten sideeffekter, så
// den kjører uten nettverk.
func TestToolRunnerRefusesCallsOverBudget(t *testing.T) {
	tools := &motorTools{s: quietServer(), state: &motorTurnState{}}
	budget := motor.Budget{Searches: 1}
	turn := &motor.Turn{Searches: 1} // kvoten er allerede brukt

	res := tools.Run(context.Background(), motor.ToolCall{Name: "web_search"}, budget, turn)

	if res.Handoff {
		t.Error("kvotestopp er ikke en overlevering")
	}
	if !strings.Contains(res.Text, "Kvoten") {
		t.Errorf("forventet ærlig kvotemelding, fikk %q", res.Text)
	}
	if res.Evidence {
		t.Error("en kvotemelding er ikke kildegrunnlag")
	}
}

// Verktøy motoren ikke eier skal gi overlevering, ikke «ukjent verktøy».
// Svarer vi modellen at verktøyet er ukjent, skriver den e-posten som ren
// tekst i chatten i stedet (målt i prod).
func TestToolRunnerHandsOffUnknownTools(t *testing.T) {
	tools := &motorTools{s: quietServer(), state: &motorTurnState{}}
	for _, name := range []string{"mail_compose", "set_widget", "setup_routine", "compose"} {
		res := tools.Run(context.Background(), motor.ToolCall{Name: name},
			motor.Budget{}, &motor.Turn{})
		if !res.Handoff {
			t.Errorf("%s eies ikke av motoren og skal gi overlevering", name)
		}
		if res.Text != "" {
			t.Errorf("%s: en overlevering skal ikke bære tekst, fikk %q", name, res.Text)
		}
	}
}

// show_table er en handling, ikke en kilde: den skal aldri telle som
// evidens, og den skal ikke vises to ganger.
func TestShowTableIsActionNotEvidence(t *testing.T) {
	var sent []string
	state := &motorTurnState{lastTable: `{"columns":["a"],"rows":[["1"]]}`}
	tools := &motorTools{s: quietServer(), state: state, emit: func(s string) { sent = append(sent, s) }}

	first := tools.Run(context.Background(), motor.ToolCall{Name: "show_table"}, motor.Budget{}, &motor.Turn{})
	if first.Evidence {
		t.Error("en visning er ikke kildegrunnlag")
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "```table") {
		t.Errorf("tabellen ble ikke sendt: %v", sent)
	}
	if !state.tableShown {
		t.Error("visningen skal merkes")
	}

	second := tools.Run(context.Background(), motor.ToolCall{Name: "show_table"}, motor.Budget{}, &motor.Turn{})
	if len(sent) != 1 {
		t.Error("tabellen skal ikke vises to ganger")
	}
	if !strings.Contains(second.Text, "query_database") {
		t.Errorf("andre visning skal peke videre, fikk %q", second.Text)
	}
}

// Relevansankeret: spørsmål + siste tanke. Dette er hele grepet som gjør at
// utdragene rangeres mot det turen LETER etter, ikke mot ordlyden i
// spørsmålet.
func TestRelevanceAnchorCombinesQuestionAndThought(t *testing.T) {
	turn := &motor.Turn{Question: "hvem konkurrerer vi med?"}
	if got := motorRelevance(turn); got != "hvem konkurrerer vi med?" {
		t.Errorf("uten tanke er ankeret spørsmålet, fikk %q", got)
	}

	turn.Thoughts = []string{"først noe annet", "nisjeimportører mot horeca"}
	got := motorRelevance(turn)
	if !strings.Contains(got, "hvem konkurrerer vi med?") || !strings.Contains(got, "nisjeimportører mot horeca") {
		t.Errorf("ankeret skal bære begge deler, fikk %q", got)
	}
	if strings.Contains(got, "først noe annet") {
		t.Error("kun SISTE tanke er ankeret — eldre tanker er utdatert hensikt")
	}

	// Tomt spørsmål (agent-/rutinekjøring) skal ikke gi en ledende linjeskift.
	if got := motorRelevance(&motor.Turn{Thoughts: []string{"bare tanke"}}); got != "bare tanke" {
		t.Errorf("uten spørsmål er ankeret tanken alene, fikk %q", got)
	}
}
