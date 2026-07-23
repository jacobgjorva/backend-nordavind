package api

import "testing"

// Simulerer at innhold strømmer inn i fragmenter og at <tool_call>-blokker
// strippes fra det klienten ser og gjøres om til kall.
func feed(fragments []string) (visible string, blocks []string) {
	var pending string
	for _, f := range fragments {
		pending += f
		safe, rest, bs := splitToolCallText(pending)
		visible += safe
		pending = rest
		blocks = append(blocks, bs...)
	}
	visible += pending // flush av uavsluttet rest
	return
}

func TestSplitToolCallText(t *testing.T) {
	cases := []struct {
		name    string
		frags   []string
		visible string
		blocks  []string
	}{
		{
			name:    "ren tekst",
			frags:   []string{"Hei ", "på ", "deg"},
			visible: "Hei på deg",
		},
		{
			name:    "hel blokk i ett stykke",
			frags:   []string{`<tool_call>{"name":"q","arguments":{}}</tool_call>`},
			visible: "",
			blocks:  []string{`{"name":"q","arguments":{}}`},
		},
		{
			name: "blokk delt over token-grenser",
			frags: []string{
				"<tool", "_call>{\"name\":", `"query_database",`,
				`"arguments":{"sql":"SELECT 1"}}`, "</tool", "_call>",
			},
			visible: "",
			blocks:  []string{`{"name":"query_database","arguments":{"sql":"SELECT 1"}}`},
		},
		{
			name:    "tekst før og etter blokk",
			frags:   []string{"før ", "<tool_call>{}", "</tool_call>", " etter"},
			visible: "før  etter",
			blocks:  []string{"{}"},
		},
		{
			name:    "vinkelparentes som ikke er tag",
			frags:   []string{"a < b og c > d"},
			visible: "a < b og c > d",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vis, blocks := feed(c.frags)
			if vis != c.visible {
				t.Errorf("visible = %q, vil ha %q", vis, c.visible)
			}
			if len(blocks) != len(c.blocks) {
				t.Fatalf("blocks = %v, vil ha %v", blocks, c.blocks)
			}
			for i := range blocks {
				if blocks[i] != c.blocks[i] {
					t.Errorf("block[%d] = %q, vil ha %q", i, blocks[i], c.blocks[i])
				}
			}
		})
	}
}

func TestParseTextualToolCall(t *testing.T) {
	name, args := parseTextualToolCall(`{"name":"query_database","arguments":{"sql":"SELECT 1","connection_id":"c1"}}`)
	if name != "query_database" {
		t.Fatalf("name = %q", name)
	}
	if args != `{"sql":"SELECT 1","connection_id":"c1"}` {
		t.Errorf("args = %q", args)
	}
	// Argumenter som JSON-streng skal pakkes ut.
	_, args2 := parseTextualToolCall(`{"name":"q","arguments":"{\"x\":1}"}`)
	if args2 != `{"x":1}` {
		t.Errorf("args2 = %q", args2)
	}
}
