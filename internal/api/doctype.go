package api

import (
	"path/filepath"
	"strings"
)

// Dokumenttype styrer INNGESTEN (KUNNSKAP-V2, 2026-08-02). Bakgrunn: all
// opplasting gikk gjennom proposisjonering, som er bygget for PROSA — den
// gjør et avsnitt om til atomære utsagn. Sluppet løs på en kildekodefil ga
// den 100 mikrolinjer à «Lukk databaseforbindelsen etter behandling av alle
// rader», mens filens docstring — der det STÅR hva filen gjør og hvilket
// prosjekt den hører til — ble malt bort. Målt i prod: spørsmålet «hvilket
// prosjekt var dette?» toppet på 0.719, godt under relevansgulvet, fordi
// svaret ikke lenger fantes i hentbar form.
//
// Typen avgjøres DETERMINISTISK av filnavnet (data, ikke modellkall), og
// hver type har sin inngest-form. Ny filtype = ny rad her.

type docKind string

const (
	kindProse docKind = "prose" // standard: proposisjonering
	kindCode  docKind = "code"  // kildekode: dokumentasjonen bevares hel
)

// codeExts er filendelsene som behandles som kildekode. Ren data.
var codeExts = map[string]bool{
	".py": true, ".go": true, ".js": true, ".ts": true, ".tsx": true,
	".jsx": true, ".rb": true, ".rs": true, ".java": true, ".kt": true,
	".c": true, ".h": true, ".cpp": true, ".cs": true, ".php": true,
	".sh": true, ".bash": true, ".zsh": true, ".sql": true, ".swift": true,
	".yaml": true, ".yml": true, ".toml": true,
}

func kindOf(filename string) docKind {
	if codeExts[strings.ToLower(filepath.Ext(filename))] {
		return kindCode
	}
	return kindProse
}

// docHeader plukker filens egen dokumentasjon: den sammenhengende blokken av
// docstring eller kommentarer øverst i fila (etter en eventuell shebang).
// Dette er den ENE biten som forklarer hva filen er — den lagres ordrett som
// sin egen kunnskapsbit. Tom streng når filen ikke har noe hode.
func docHeader(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && (strings.TrimSpace(lines[i]) == "" ||
		strings.HasPrefix(lines[i], "#!") ||
		strings.HasPrefix(strings.TrimSpace(lines[i]), "# -*-")) {
		i++
	}
	if i >= len(lines) {
		return ""
	}
	first := strings.TrimSpace(lines[i])

	// Avgrenset blokk: """…""", '''…''' eller /* … */
	for _, d := range []struct{ open, close string }{
		{`"""`, `"""`}, {"'''", "'''"}, {"/*", "*/"},
	} {
		if !strings.HasPrefix(first, d.open) {
			continue
		}
		var b strings.Builder
		rest := strings.TrimPrefix(first, d.open)
		if j := strings.Index(rest, d.close); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		b.WriteString(rest)
		for k := i + 1; k < len(lines); k++ {
			if j := strings.Index(lines[k], d.close); j >= 0 {
				b.WriteString("\n" + lines[k][:j])
				return strings.TrimSpace(b.String())
			}
			b.WriteString("\n" + lines[k])
			if b.Len() > headerMaxChars {
				break
			}
		}
		return strings.TrimSpace(b.String())
	}

	// Linjekommentarer: sammenhengende // eller # øverst.
	for _, p := range []string{"//", "#"} {
		if !strings.HasPrefix(first, p) {
			continue
		}
		var b strings.Builder
		for k := i; k < len(lines); k++ {
			t := strings.TrimSpace(lines[k])
			if !strings.HasPrefix(t, p) {
				break
			}
			b.WriteString(strings.TrimSpace(strings.TrimPrefix(t, p)) + "\n")
			if b.Len() > headerMaxChars {
				break
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

// headerMaxChars: et filhode er en forklaring, ikke et kapittel.
const headerMaxChars = 2000

// codeNotes bygger kunnskapsbitene for en kildekodefil: filens egen
// dokumentasjon som ÉN hel bit først, deretter kodeseksjonene ordrett.
// Ingen modellkall, ingen omskriving — kode taper mening av å bli
// parafrasert, og hentingen er bedre på ekte kode enn på gjenfortelling.
func codeNotes(filename, text string) []string {
	var out []string
	if h := docHeader(text); h != "" {
		out = append(out, "Om filen "+filename+": "+h)
	}
	out = append(out, chunkText(text)...)
	return out
}
