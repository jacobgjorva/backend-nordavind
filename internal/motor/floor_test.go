package motor

import (
	"strings"
	"testing"
)

// Svarene under er ORDRETT fra ekte kjøringer (loop_v5/, loop_runs_final/).
// Modellen uthever kandidatnavn med ** uansett hva instruksen sier — det er
// nettopp derfor stripping må være et gulv og ikke en promptregel.
const realBoldAnswer = "For et lite byggefirma som må dokumentere HMS, er **HMS Bygg fra West " +
	"Internkontroll** et godt valg. Systemet er skreddersydd for byggebransjen, og koster 2 990 " +
	"kroner per år."

func TestStripEmphasisRemovesMarkersKeepsText(t *testing.T) {
	got := StripEmphasis(realBoldAnswer)
	if strings.Contains(got, "**") {
		t.Errorf("fet skrift står igjen: %q", got)
	}
	if !strings.Contains(got, "HMS Bygg fra West Internkontroll") {
		t.Errorf("teksten forsvant med markørene: %q", got)
	}
	if !strings.Contains(got, "2 990") {
		t.Error("tallet skal overleve strippingen")
	}
}

func TestStripEmphasisFlattensHeadings(t *testing.T) {
	got := StripEmphasis("### Anbefaling\nDu bør velge X.\n## Hvorfor\nFordi Y.")
	if strings.Contains(got, "#") {
		t.Errorf("overskrifter står igjen: %q", got)
	}
	for _, want := range []string{"Anbefaling", "Du bør velge X.", "Hvorfor", "Fordi Y."} {
		if !strings.Contains(got, want) {
			t.Errorf("mistet %q i %q", want, got)
		}
	}
}

// Kodeblokker er innhold: en tabell-JSON eller kodeeksempel skal aldri
// røres av en tekstoperasjon som er ment for prosa.
func TestStripEmphasisLeavesCodeFencesAlone(t *testing.T) {
	in := "Her er tabellen:\n```table\n{\"columns\":[\"**a**\"],\"rows\":[[\"# 1\"]]}\n```\nOg **dette** er prosa."
	got := StripEmphasis(in)
	if !strings.Contains(got, `{"columns":["**a**"],"rows":[["# 1"]]}`) {
		t.Errorf("kodeblokken ble endret: %q", got)
	}
	if strings.Contains(got, "**dette**") {
		t.Errorf("prosaen utenfor blokken skulle vært strippet: %q", got)
	}
}

func TestStripEmphasisHandlesPlainText(t *testing.T) {
	plain := "Styringsrenten er 4,25 prosent."
	if got := StripEmphasis(plain); got != plain {
		t.Errorf("ren tekst skal stå urørt, fikk %q", got)
	}
	if got := StripEmphasis(""); got != "" {
		t.Errorf("tom tekst skal forbli tom, fikk %q", got)
	}
}

// --- leveranserekkefølgen ------------------------------------------------

type recordingFloors struct{ order []string }

func (f *recordingFloors) Table(*Turn)                { f.order = append(f.order, "table") }
func (f *recordingFloors) Insight(*Turn, string, int) { f.order = append(f.order, "insight") }
func (f *recordingFloors) SourceNote(*Turn, string)   { f.order = append(f.order, "source") }
func (f *recordingFloors) NextStep(*Turn, string)     { f.order = append(f.order, "next") }

// Rekkefølgen ER kontrakten. Bytter den, får brukeren observasjonen før
// tabellen den kommenterer.
func TestDeliverRunsFloorsInContractOrder(t *testing.T) {
	out := &fakeOut{}
	floors := &recordingFloors{}
	DeliverWithFloors(out, floors, &Turn{}, realBoldAnswer, 700)

	want := []string{"table", "insight", "source", "next"}
	if strings.Join(floors.order, ",") != strings.Join(want, ",") {
		t.Errorf("feil rekkefølge: %v", floors.order)
	}
	if len(out.content) != 1 || strings.Contains(out.content[0], "**") {
		t.Errorf("svaret skal sendes strippet: %v", out.content)
	}
	if !out.done {
		t.Error("strømmen skal avsluttes")
	}
}

// Uten gulv (og med tomt svar) skal leveransen fortsatt avslutte rent.
func TestDeliverWithoutFloorsStillCloses(t *testing.T) {
	out := &fakeOut{}
	DeliverWithFloors(out, nil, &Turn{}, "", 0)
	if len(out.content) != 0 {
		t.Errorf("tomt svar skal ikke sende innhold: %v", out.content)
	}
	if !out.done {
		t.Error("strømmen skal avsluttes uansett")
	}
}

// Den ærlige bunnen skiller «prøvde ingenting» fra «fant ingenting», og lar
// databasens egen redegjørelse vinne når den finnes.
func TestHonestEmptyDistinguishesWhatWasTried(t *testing.T) {
	if got := HonestEmpty(&Turn{}, ""); !strings.Contains(got, "formulere det litt annerledes") {
		t.Errorf("uten verktøybruk: %q", got)
	}
	if got := HonestEmpty(&Turn{UsedTool: true}, ""); !strings.Contains(got, "kildene jeg har") {
		t.Errorf("etter verktøybruk: %q", got)
	}
	explain := "Spørringen mot Kunder feilet: tabellen finnes ikke."
	if got := HonestEmpty(&Turn{UsedTool: true}, explain); got != explain {
		t.Errorf("databasens egen redegjørelse skal vinne, fikk %q", got)
	}
}
