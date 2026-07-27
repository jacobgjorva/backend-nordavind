package api

import (
	"strings"
	"testing"
)

// Utagget verktøykall i prosaen («…«tekoligark»fetch_url{…») skal klippes —
// og alt etter det, siden resten er verktøysyntaks, ikke svar.
func TestCutBareToolCall(t *testing.T) {
	cases := map[string]string{
		`Årets ord i 2025 var «tekoligark»fetch_url{"url": "https://x"}`: "Årets ord i 2025 var «tekoligark»",
		`Styringsrenten er 4,25 %. web_search {"query": "mer"}`:          "Styringsrenten er 4,25 %.",
		`Helt vanlig svar uten kall.`:                                    "Helt vanlig svar uten kall.",
		// Falsk positiv-vern: navnet uten { skal aldri klippe.
		`Du kan bruke web_search når du trenger ferske tall.`: `Du kan bruke web_search når du trenger ferske tall.`,
	}
	for in, want := range cases {
		if got := cutBareToolCall(in); got != want {
			t.Errorf("cutBareToolCall(%q)\n  = %q\n  vil %q", in, got, want)
		}
	}
}

// Streaming: et halvskrevet verktøynavn holdes igjen til neste chunk, ellers
// rekker «…fetch_ur» ut til brukeren før { avslører hva det var.
func TestBareToolCallHeldBackWhileStreaming(t *testing.T) {
	safe, rest, _ := splitToolCallText("Svaret er ferdig. fetch_ur")
	if strings.Contains(safe, "fetch_ur") {
		t.Fatalf("halvt verktøynavn ble sendt: %q", safe)
	}
	if !strings.Contains(rest, "fetch_ur") {
		t.Fatalf("halvt verktøynavn ble ikke holdt igjen: rest=%q", rest)
	}
	// Vanlig tekst skal ikke holdes igjen unødig.
	safe2, rest2, _ := splitToolCallText("Et helt vanlig svar.")
	if safe2 != "Et helt vanlig svar." || rest2 != "" {
		t.Fatalf("vanlig tekst ble holdt igjen: safe=%q rest=%q", safe2, rest2)
	}
}
