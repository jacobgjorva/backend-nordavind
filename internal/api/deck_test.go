package api

import "testing"

// Kjernen i presentasjonsflyten: en patch rører KUN feltene den nevner.
// Brukeren kan ha rettet tittelen selv i canvaset — den skal overleve at
// modellen etterpå endrer brødteksten på samme slide.
func TestApplySlideFieldsKeepsUntouchedFields(t *testing.T) {
	slide := map[string]any{
		"id": "s1", "layout": "bullets",
		"title": "Brukerens egen tittel", "content": "- ett\n- to",
	}
	applySlideFields(slide, map[string]any{"content": "- tre\n- fire"})
	if slide["title"] != "Brukerens egen tittel" {
		t.Errorf("tittelen ble overskrevet: %v", slide["title"])
	}
	if slide["content"] != "- tre\n- fire" {
		t.Errorf("innholdet ble ikke oppdatert: %v", slide["content"])
	}
	if slide["layout"] != "bullets" {
		t.Errorf("layouten forsvant: %v", slide["layout"])
	}
}

// Tom streng er måten et felt fjernes på (f.eks. en bildetekst).
func TestApplySlideFieldsRemovesOnEmpty(t *testing.T) {
	slide := map[string]any{"id": "s1", "title": "Noe", "content": "tekst"}
	applySlideFields(slide, map[string]any{"title": ""})
	if _, ok := slide["title"]; ok {
		t.Error("tittelen skulle vært fjernet")
	}
	if slide["content"] != "tekst" {
		t.Error("innholdet skulle stått urørt")
	}
}

// Ukjente felter fra modellen skal ikke havne i specen.
func TestApplySlideFieldsIgnoresUnknown(t *testing.T) {
	slide := map[string]any{"id": "s1"}
	applySlideFields(slide, map[string]any{"op": "set", "id": "annen", "tull": 1})
	if slide["id"] != "s1" {
		t.Errorf("id-en ble endret: %v", slide["id"])
	}
	if _, ok := slide["tull"]; ok {
		t.Error("ukjent felt ble lagret")
	}
}

func TestInsertSlidePlacement(t *testing.T) {
	slides := []map[string]any{{"id": "a"}, {"id": "b"}}
	got := insertSlide(slides, map[string]any{"id": "c"}, "a")
	if len(got) != 3 || got[1]["id"] != "c" {
		t.Fatalf("ny slide havnet feil: %v", ids(got))
	}
	// Ukjent «after» legger bakerst i stedet for å miste sliden.
	got = insertSlide(got, map[string]any{"id": "d"}, "finnes-ikke")
	if got[len(got)-1]["id"] != "d" {
		t.Fatalf("ukjent after ga feil plassering: %v", ids(got))
	}
}

func TestIndexOfSlide(t *testing.T) {
	slides := []map[string]any{{"id": "a"}, {"id": "b"}}
	if indexOfSlide(slides, "b") != 1 {
		t.Error("fant ikke sliden")
	}
	if indexOfSlide(slides, "") != -1 {
		t.Error("tom id skal aldri treffe")
	}
}

func ids(slides []map[string]any) []string {
	out := make([]string, len(slides))
	for i, s := range slides {
		out[i], _ = s["id"].(string)
	}
	return out
}
