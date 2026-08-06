package api

import (
	"regexp"
	"strings"
)

// Drive-stil (avtalt 2026-08-06): hvert svar skal slutte med holdning eller
// fremdrift — aldri et generisk «si fra om du lurer på noe». Prompten og
// drive-evalen (cmd/drive-eval) trener stilen; dette vernet garanterer i kode
// at de kjente oppslagsverk-avslutningene aldri når brukeren, uansett
// modellens dagsform.

// genericCloserRe matcher en HEL avsluttende setning som kun er høflig fyll.
// Bevisst smal: bare formuleringer som aldri bærer innhold. Vurderinger og
// konkrete tilbud («vil du at jeg bryter det ned per kunde?») røres aldri.
var genericCloserRe = regexp.MustCompile(`(?i)^(bare )?(si (gjerne )?(i)?fra|gi (meg )?(gjerne )?beskjed|ta (gjerne )?kontakt|ikke nøl med å (spørre|ta kontakt)|spør (meg )?gjerne|håper (dette|det) (hjelper|var til hjelp)|la meg (få )?vite)( (om|hvis|dersom|når) [^.!?]*)?[.!?…]?$`)

// stripGenericCloser klipper generiske sluttsetninger fra et bufret svar.
// Flere på rad klippes alle («Håper det hjelper! Si fra om noe er uklart.»);
// stopper straks en setning med innhold møtes. Består svaret KUN av fyll,
// beholdes det urørt — et tomt svar er verre enn et høflig.
func stripGenericCloser(s string) string {
	trimmed := strings.TrimRight(s, " \n\t")
	for {
		cut := lastSentenceStart(trimmed)
		if cut < 0 {
			break
		}
		last := strings.TrimSpace(trimmed[cut:])
		if !genericCloserRe.MatchString(last) {
			break
		}
		next := strings.TrimRight(trimmed[:cut], " \n\t")
		if strings.TrimSpace(next) == "" {
			return s
		}
		trimmed = next
	}
	return trimmed
}

// lastSentenceStart finner starten på siste setning (etter siste .!?… eller
// linjeskift som ikke er helt på slutten). -1 når det bare er én setning.
func lastSentenceStart(s string) int {
	r := []rune(s)
	for i := len(r) - 1; i >= 0; i-- {
		c := r[i]
		if (c == '.' || c == '!' || c == '?' || c == '…' || c == '\n') && i < len(r)-1 {
			return len(string(r[:i+1]))
		}
	}
	return -1
}
