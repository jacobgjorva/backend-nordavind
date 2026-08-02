package api

import (
	"strings"
	"testing"
)

// Kodefila fra prod-bommen (2026-08-02): docstringen bærer BÅDE hva filen
// gjør og hvilket prosjekt den hører til. Den må overleve som én hel bit.
const pyFile = `#!/usr/bin/env python3
"""Klassifiser svar fra kommunene ut fra selve meldingsteksten, ikke bare emnet.

scan_svar.py skiller kun autosvar (emnebasert) fra "et reelt svar".
"""
import re

def klassifiser(emne, kropp):
    return "ny"
`

func TestCodeFilesKeepTheirDocumentationWhole(t *testing.T) {
	if kindOf("klassifiser_svar.py") != kindCode {
		t.Fatal(".py skal være kode")
	}
	if kindOf("rutine.pdf") != kindProse || kindOf("varemottak.docx") != kindProse {
		t.Fatal("dokumenter skal være prosa")
	}
	notes := codeNotes("klassifiser_svar.py", pyFile)
	if len(notes) == 0 {
		t.Fatal("ingen biter")
	}
	// Første bit er filens egen dokumentasjon, hel og ordrett.
	if !strings.Contains(notes[0], "klassifiser_svar.py") ||
		!strings.Contains(notes[0], "Klassifiser svar fra kommunene") ||
		!strings.Contains(notes[0], "scan_svar.py skiller kun autosvar") {
		t.Fatalf("docstringen overlevde ikke hel: %q", notes[0])
	}
	// Ingen shredding: en liten fil skal gi FÅ biter, ikke hundre.
	if len(notes) > 4 {
		t.Fatalf("for finhakket: %d biter", len(notes))
	}
}

func TestDocHeaderVariants(t *testing.T) {
	if got := docHeader("// Pakke foo gjør X.\n// Andre linje.\npackage foo\n"); !strings.Contains(got, "Andre linje") {
		t.Fatalf("go-kommentarer: %q", got)
	}
	if got := docHeader("#!/bin/sh\n# Rydder gamle logger.\n# Kjøres nattlig.\nrm -f *.log\n"); !strings.Contains(got, "nattlig") {
		t.Fatalf("shell etter shebang: %q", got)
	}
	if got := docHeader("/* Modul for eksport.\n   Brukes av rapporten. */\nconst a = 1;\n"); !strings.Contains(got, "rapporten") {
		t.Fatalf("blokk-kommentar: %q", got)
	}
	if got := docHeader("package main\n\nfunc main() {}\n"); got != "" {
		t.Fatalf("uten hode skal være tomt: %q", got)
	}
}
