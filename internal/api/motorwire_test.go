package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Flagget er den viktigste garantien i del 1: uten ENGINE=v6 skal ingenting
// endres. Default fra config må være av.
func TestEngineFlagDefaultsOff(t *testing.T) {
	t.Setenv("UPSTREAM_API_KEY", "test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.Engine != "" {
		t.Errorf("ENGINE skal være tom som standard, fikk %q", cfg.Engine)
	}
	if (&Server{cfg: cfg}).motorOn() {
		t.Error("motoren skal være av uten ENGINE=v6")
	}
	cfg.Engine = "v6"
	if !(&Server{cfg: cfg}).motorOn() {
		t.Error("ENGINE=v6 skal slå motoren på")
	}
	cfg.Engine = "V6 "
	if (&Server{cfg: cfg}).motorOn() {
		t.Error("kun eksakt «v6» slår på — aldri en løs tolkning av verdien")
	}
}

// Emitteren må produsere NØYAKTIG dagens SSE-format; frontend leser det.
func TestEmitterUsesExistingSSEFormat(t *testing.T) {
	var sent []string
	e := apiEmitter{emit: func(s string) { sent = append(sent, s) }}

	e.Content("hei")
	e.Content("   ") // tomt innhold skal aldri gi en ramme
	e.Sources([]motor.Source{{Title: "Norges Bank", URL: "https://nb.no"}})
	e.Sources(nil)
	e.Done()

	if len(sent) != 3 {
		t.Fatalf("forventet 3 rammer (innhold, kilder, done), fikk %d: %v", len(sent), sent)
	}
	if sent[0] != contentSSE("hei") {
		t.Errorf("innhold avviker fra dagens format: %q", sent[0])
	}
	var meta struct {
		Sources []sourceRef `json:"nordavind_sources"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(sent[1], "data: ")), &meta); err != nil {
		t.Fatalf("kilderamme er ikke gyldig JSON: %v", err)
	}
	if len(meta.Sources) != 1 || meta.Sources[0].Title != "Norges Bank" {
		t.Errorf("kilder kom ikke gjennom: %+v", meta.Sources)
	}
	if sent[2] != "data: [DONE]" {
		t.Errorf("avslutning avviker: %q", sent[2])
	}
}

// Kostnadsklassene er koblingen mellom verktøynavn og budsjett. Bommer de,
// blir en kvote stille uvirksom.
func TestToolKindsCoverBudgetedTools(t *testing.T) {
	want := map[string]motor.ToolKind{
		"web_search":     motor.KindSearch,
		"fetch_url":      motor.KindFetch,
		"query_database": motor.KindData,
		"show_table":     motor.KindShow,
		"mail_compose":   motor.KindOther, // ukjent for motoren → handoff senere
	}
	for name, kind := range want {
		if got := motorToolKind(name); got != kind {
			t.Errorf("%s: forventet klasse %v, fikk %v", name, kind, got)
		}
	}
}

// Evidens-settet avgjør hva tallkontrollen måler svaret mot. Det må aldri
// være VIDERE enn legacys leste verktøy — da ville motoren godta grunnlag
// legacy avviser. Smalere er tillatt og tilsiktet: handlingsverktøy
// returnerer en kvittering («Tabellen er vist for brukeren»), ikke kilder,
// og et slikt resultat i grunnlaget gjør bare kontrollen slappere.
func TestEvidenceToolsAreSubsetOfLegacyReadTools(t *testing.T) {
	for name := range evidenceTools {
		if !legacyReadTools[name] {
			t.Errorf("%s er kildegrunnlag i motoren, men ikke lest verktøy i legacy", name)
		}
	}
	// De bevisste utelatelsene. Endres lista i legacy, skal denne testen
	// tvinge fram et valg i stedet for stille drift.
	for _, action := range []string{"show_table", "list_agents"} {
		if evidenceTools[action] {
			t.Errorf("%s er et handlingsverktøy og skal ikke telle som kildegrunnlag", action)
		}
		if !legacyReadTools[action] {
			t.Errorf("legacy har mistet %s — vurder om utelatelsen fortsatt gir mening", action)
		}
	}
}
