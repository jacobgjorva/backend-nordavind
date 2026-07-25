package api

import "testing"

func TestDedupeStutter(t *testing.T) {
	in := "Den nyligste salgsraden er fra Pernod Ricard den 31. desember 2023, med 3896 enheter solgt til kunde Den nyligste salgsraden er fra Pernod Ricard den 31. desember 2023, med 3896 enheter solgt til kunde 22894."
	out := dedupeStutter(in)
	if out != "Den nyligste salgsraden er fra Pernod Ricard den 31. desember 2023, med 3896 enheter solgt til kunde 22894." {
		t.Fatalf("stamming ikke fjernet: %q", out)
	}
	if got := dedupeStutter("Helt vanlig svar uten gjentakelse."); got != "Helt vanlig svar uten gjentakelse." {
		t.Fatalf("vanlig svar skal være urørt, fikk %q", got)
	}
}
