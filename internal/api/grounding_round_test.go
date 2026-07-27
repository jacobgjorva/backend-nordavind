package api

import "testing"

func TestParseLooseNumber(t *testing.T) {
	cases := map[string]float64{
		"6594.90":    6594.90,
		"6 595":      6595,
		"2835196,20": 2835196.20,
		"12":         12,
	}
	for in, want := range cases {
		got, ok := parseLooseNumber(in)
		if !ok || got != want {
			t.Errorf("parseLooseNumber(%q) = %v,%v ventet %v", in, got, ok, want)
		}
	}
}

func TestIsRoundingOfAcceptsRealRounding(t *testing.T) {
	if !isRoundingOf("6 595", []string{"6594.90"}) {
		t.Error("6 595 skal godtas som avrunding av 6594,90")
	}
	if !isRoundingOf("2 494 741", []string{"2494741.06"}) {
		t.Error("avrunding av øre skal godtas")
	}
}

func TestIsRoundingOfRejectsInvention(t *testing.T) {
	if isRoundingOf("7 200", []string{"6594.90"}) {
		t.Error("et helt annet tall skal ikke passere som avrunding")
	}
	if isRoundingOf("6 700", []string{"6594.90"}) {
		t.Error("avvik utenfor avrunding skal ikke passere")
	}
}
