package api

import "testing"

func TestEmojiAllowedKeptDisallowedStripped(t *testing.T) {
	got := enforceEmojiMessage("Klart det 😌 og 🚀 er ute.")
	if got != "Klart det 😌 og  er ute." {
		t.Fatalf("uventet: %q", got)
	}
}

func TestEmojiBudgetMaxOne(t *testing.T) {
	got := enforceEmojiMessage("Skål 🥂 og seier ✌️!")
	if got != "Skål 🥂 og seier !" {
		t.Fatalf("budsjettet skal stoppe på én, fikk %q", got)
	}
}

func TestEmojiVariationSelectorKept(t *testing.T) {
	got := enforceEmojiMessage("Da er vi enige ✌️")
	if got != "Da er vi enige ✌️" {
		t.Fatalf("✌️ med variantvelger skal bestå, fikk %q", got)
	}
}

func TestEmojiZWJAndSkinToneSequencesRemoved(t *testing.T) {
	got := enforceEmojiMessage("Bra jobba 👍🏽 av 👨‍👩‍👧 og 🤝🏿!")
	if got != "Bra jobba  av  og !" {
		t.Fatalf("sekvenser med hudtone/ZWJ skal strykes helt, fikk %q", got)
	}
}

func TestEmojiPlainTextUntouched(t *testing.T) {
	in := "Æ, ø og å – tall som 3,14 og «sitat» skal stå urørt."
	if got := enforceEmojiMessage(in); got != in {
		t.Fatalf("ren tekst skal aldri endres, fikk %q", got)
	}
}

func TestEmojiStreamedBudgetSharedAcrossSentences(t *testing.T) {
	g := newSentenceGate([]string{"kilde"})
	out := g.feed("Fint 😌. ")
	out += g.feed("Mer 🥂. ")
	// Halen (" ") holdes i gaten til neste setning — det er ventet.
	if out != "Fint 😌. Mer ." {
		t.Fatalf("budsjettet skal deles over hele meldingen, fikk %q", out)
	}
}
