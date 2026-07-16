package router

import "testing"

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
		{"lang melding", string(make([]byte, 700)), HeavyModel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Pick([]Message{{Role: "user", Content: c.text}})
			if got != c.want {
				t.Errorf("Pick(%q) = %q, vil ha %q", c.text, got, c.want)
			}
		})
	}
}
