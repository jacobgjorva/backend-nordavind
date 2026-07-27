package api

import (
	"strings"
	"testing"
)

func TestUsedSourcesOnlySuccessful(t *testing.T) {
	got := usedSources([]dbAttempt{
		{outcome: dbOK, conn: "Mjødhallen ERP"},
		{outcome: dbFailed, conn: "Kunder"},
		{outcome: dbOK, conn: "Mjødhallen ERP"},
	})
	if len(got) != 1 || got[0] != "Mjødhallen ERP" {
		t.Fatalf("ventet kun den vellykkede kilden, fikk %v", got)
	}
}

func TestSourceNoteNamesSingleSource(t *testing.T) {
	var sent []string
	n := newNarrator(func(l string) { sent = append(sent, l) }, true, nil)
	n.sourceNote(n.emit, "Valhall Arena er størst.", []string{"Mjødhallen ERP"}, true)
	if len(sent) != 1 || !strings.Contains(sent[0], "Mjødhallen ERP") {
		t.Fatalf("kilden ble ikke navngitt: %v", sent)
	}
}

func TestSourceNoteWarnsOnMixing(t *testing.T) {
	var sent []string
	n := newNarrator(func(l string) { sent = append(sent, l) }, true, nil)
	n.sourceNote(n.emit, "Tall.", []string{"Kunder", "Mjødhallen ERP"}, true)
	if len(sent) != 1 || !strings.Contains(sent[0], "flere kilder") {
		t.Fatalf("blandingsvarsel mangler: %v", sent)
	}
}

// Én kilde totalt: ingen merknad — det finnes ikke noe alternativ å forveksle med.
func TestSourceNoteSilentWithSingleConnection(t *testing.T) {
	var sent []string
	n := newNarrator(func(l string) { sent = append(sent, l) }, true, nil)
	n.sourceNote(n.emit, "Tall.", []string{"Mjødhallen ERP"}, false)
	if len(sent) != 0 {
		t.Fatalf("skal være stille med én kilde: %v", sent)
	}
}

// Svaret nevner alt kilden: ikke gjenta.
func TestSourceNoteSilentWhenNamed(t *testing.T) {
	var sent []string
	n := newNarrator(func(l string) { sent = append(sent, l) }, true, nil)
	n.sourceNote(n.emit, "Ifølge Mjødhallen ERP er Valhall størst.", []string{"Mjødhallen ERP"}, true)
	if len(sent) != 0 {
		t.Fatalf("kilden er alt nevnt, ikke gjenta: %v", sent)
	}
}
