package motor

import "testing"

// Porten er den viktigste enkeltregelen i del 1: en utfører-flyt som
// havner i motoren mister leveransekontrakten sin stille.
func TestTakesRejectsExecutorFlowsAndModes(t *testing.T) {
	for _, flow := range []string{"create_widget", "export_excel", "email", "create_routine"} {
		if Takes(map[string]any{}, flow) {
			t.Errorf("%s har egen leveransekontrakt og skal ikke tas av motoren", flow)
		}
	}
	for _, field := range modeFields {
		if Takes(map[string]any{field: true}, "free_chat") {
			t.Errorf("%s er en spesialmodus og eier turen selv", field)
		}
	}
	for _, flow := range []string{"free_chat", "data_question", "web_fact", "research_relation", "recommendation", "smalltalk"} {
		if !Takes(map[string]any{}, flow) {
			t.Errorf("%s er vanlig chat og skal tas av motoren", flow)
		}
	}
}

// Et tomt/falskt modusflagg er ikke en modus — payloaden bærer feltet på
// hver eneste tur fra noen klienter.
func TestTakesIgnoresEmptyModeFields(t *testing.T) {
	for _, v := range []any{nil, "", false} {
		if !Takes(map[string]any{"nordavind_widget": v}, "free_chat") {
			t.Errorf("tomt modusfelt (%v) skal ikke stenge motoren ute", v)
		}
	}
}

func TestBudgetStopsAtCeiling(t *testing.T) {
	b := Budget{Searches: 2, Fetches: 1}
	if !b.Allow(KindSearch, 1) {
		t.Error("søk 2 av 2 skal være tillatt")
	}
	if b.Allow(KindSearch, 2) {
		t.Error("søk 3 av 2 skal avvises")
	}
	if b.Allow(KindFetch, 1) {
		t.Error("henting 2 av 1 skal avvises")
	}
	// Verktøy uten kostnadsklasse (vis tabell, db) begrenses ikke her.
	if !b.Allow(KindShow, 99) {
		t.Error("visning har ingen kvote i budsjettet")
	}
}

// Thought/Done skiller de to bruksmåtene av Text. Blandes de, ender
// sluttsvaret som arbeidsnarrasjon i chatten (målt i v3).
func TestResponseSeparatesThoughtFromAnswer(t *testing.T) {
	working := ModelResponse{Text: " leter etter segmentet ", Calls: []ToolCall{{Name: "web_search"}}}
	if working.Done() {
		t.Error("runde med verktøykall er ikke ferdig")
	}
	if working.Thought() != "leter etter segmentet" {
		t.Errorf("tanken skal trimmes og returneres, fikk %q", working.Thought())
	}
	final := ModelResponse{Text: "Svaret er 4,25 prosent."}
	if !final.Done() {
		t.Error("runde uten verktøykall er ferdig")
	}
	if final.Thought() != "" {
		t.Error("et sluttsvar er aldri en tanke")
	}
}

func TestTurnRecordsSpendAndEvidence(t *testing.T) {
	turn := &Turn{}
	turn.Record(ToolResult{Kind: KindSearch, Evidence: true, Text: "kilde A",
		Sources: []Source{{Title: "A", URL: "https://a"}}})
	turn.Record(ToolResult{Kind: KindFetch, Evidence: true, Text: "side B"})
	turn.Record(ToolResult{Kind: KindShow, Text: "Tabellen er vist."})

	if turn.Searches != 1 || turn.Fetches != 1 {
		t.Errorf("forbruk feiltalt: søk=%d hent=%d", turn.Searches, turn.Fetches)
	}
	if !turn.UsedTool {
		t.Error("turen brukte verktøy")
	}
	if len(turn.Evidence) != 2 {
		t.Errorf("kun kildegrunnlag er evidens, fikk %d", len(turn.Evidence))
	}
	if len(turn.Sources) != 1 {
		t.Errorf("kilder skal samles, fikk %d", len(turn.Sources))
	}
	if turn.Spent(KindSearch) != 1 {
		t.Error("Spent skal speile telleren")
	}
}

// Tomt verktøyresultat er ikke evidens: en tom streng i grunnlaget gjør
// tallkontrollen blind uten å tilføre noe.
func TestTurnIgnoresEmptyEvidence(t *testing.T) {
	turn := &Turn{}
	turn.Record(ToolResult{Kind: KindSearch, Evidence: true, Text: "   "})
	if len(turn.Evidence) != 0 {
		t.Error("tomt resultat skal ikke telle som kildegrunnlag")
	}
}

func TestLastThoughtIsRelevanceAnchor(t *testing.T) {
	turn := &Turn{}
	if turn.LastThought() != "" {
		t.Error("uten tanker er ankeret tomt")
	}
	turn.Thoughts = append(turn.Thoughts, "først", "sist")
	if turn.LastThought() != "sist" {
		t.Errorf("ankeret er siste tanke, fikk %q", turn.LastThought())
	}
}
