package router

import (
	"encoding/json"
	"testing"
)

func strContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func TestPick(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"trivielt", "Hva er hovedstaden i Norge?", MidModel},
		{"smalltalk", "hei, hvordan går det?", MidModel},
		{"analyse", "Analyser salgstallene våre for Q2", HeavyModel},
		{"sammenligning", "Sammenlign React og SolidJS for vår bruk", HeavyModel},
		{"hvorfor", "Hvorfor faller marginen når volumet øker?", HeavyModel},
		{"lang melding", string(make([]rune, 700)), HeavyModel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Pick([]Message{{Role: "user", Content: strContent(c.text)}})
			if got != c.want {
				t.Errorf("Pick(%q) = %q, vil ha %q", c.text, got, c.want)
			}
		})
	}
}

func TestHasImageAndText(t *testing.T) {
	imgContent := json.RawMessage(`[
		{"type":"text","text":"Hva viser bildet?"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}
	]`)
	msgs := []Message{{Role: "user", Content: imgContent}}
	if !LastUserHasImage(msgs) {
		t.Fatal("LastUserHasImage = false, vil ha true")
	}
	if got := msgs[0].text(); got != "Hva viser bildet? " {
		t.Errorf("text() = %q", got)
	}
	// Oppfølging uten nytt bilde skal ikke rutes til vision-modellen.
	followup := []Message{
		{Role: "user", Content: imgContent},
		{Role: "assistant", Content: strContent("Et skjermbilde.")},
		{Role: "user", Content: strContent("Noe som utmerker seg?")},
	}
	if LastUserHasImage(followup) {
		t.Error("LastUserHasImage = true for tekst-oppfølging")
	}
}
