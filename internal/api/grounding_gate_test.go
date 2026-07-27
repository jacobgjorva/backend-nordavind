package api

import (
	"strings"
	"testing"
)

// Båt-caset fra 26. juli: kontrakten (i samtalen) har 1990, 499 000 og
// 14. juni 2024 — men IKKE brukerens alder. «da var du 24 år» skal holdes
// igjen, og «24» skal aldri dekkes av at «2024» finnes (substring-fella).
func boatBasis() []string {
	return []string{
		"Klarer du å se hva dette er?\nKJØPEKONTRAKT — Bavaria 390 seilbåt, byggeår 1990. " +
			"Kjøpesum: 499 000 NOK. Overtakelse: 14. juni 2024 i Son.",
		"Da kjøpte jeg båten når jeg var 23 ojj",
	}
}

func feedInChunks(g *sentenceGate, text string, size int) string {
	var out strings.Builder
	for i := 0; i < len(text); i += size {
		end := i + size
		if end > len(text) {
			end = len(text)
		}
		out.WriteString(g.feed(text[i:end]))
	}
	return out.String()
}

func TestGateHoldsFabricatedAge(t *testing.T) {
	g := newSentenceGate(boatBasis())
	rel := feedInChunks(g, "Du kjøpte båten i juni 2024, da var du 24 år. Gratulerer med kjøpet.", 5)
	rel += g.finish()
	if !g.held {
		t.Fatalf("diktet alder skulle holdes igjen, alt slapp gjennom: %q", rel)
	}
	if strings.Contains(rel, "24 år") {
		t.Fatalf("den udekkede påstanden ble vist: %q", rel)
	}
	found := false
	for _, o := range g.offenders {
		if normDigits(o) == "24" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skulle flagget 24, fikk %v", g.offenders)
	}
}

func TestGateReleasesTraceableFacts(t *testing.T) {
	g := newSentenceGate(boatBasis())
	text := "Overtakelsen var 14. juni 2024, og prisen var 499 000 NOK. Du sa selv at du var 23."
	rel := feedInChunks(g, text, 3) + g.finish()
	if g.held {
		t.Fatalf("kildefaste verdier skulle passere, holdt igjen med avvik %v", g.offenders)
	}
	if rel != text {
		t.Fatalf("alt skulle vært sluppet, fikk %q", rel)
	}
}

// Ny kontrakt (buffer per setning): en KOMPLETT prosa-setning uten påstand
// slippes straks den er ferdig. En ufullstendig setning holdes til punktum —
// bevisst, så en påstand som dukker opp sent aldri vises halvveis.
func TestGateReleasesCompleteProse(t *testing.T) {
	g := newSentenceGate(boatBasis())
	if rel := g.feed("Det høres ut som en fin plan, og jeg er "); rel != "" {
		t.Fatalf("ufullstendig setning ble sluppet før punktum: %q", rel)
	}
	if rel := g.feed("helt enig. "); rel == "" {
		t.Fatal("komplett prosa-setning ble ikke sluppet")
	}
}

func TestGateHoldsFabricatedTailWithoutTerminator(t *testing.T) {
	g := newSentenceGate(boatBasis())
	rel := g.feed("Renten på lånet ditt er 87 prosent")
	rel += g.finish()
	if !g.held {
		t.Fatalf("udekket tall i hale uten punktum skulle holdes, slapp: %q", rel)
	}
}

func TestGateHoldsFabricatedURL(t *testing.T) {
	g := newSentenceGate(boatBasis())
	rel := g.feed("Se dokumentet her: https://example.com/kontrakt.pdf — det forklarer alt. ")
	rel += g.finish()
	if !g.held {
		t.Fatalf("diktet lenke skulle holdes igjen, slapp: %q", rel)
	}
}

func TestGateShownPlusHeldEqualsAll(t *testing.T) {
	g := newSentenceGate(boatBasis())
	text := "Kontrakten gjaldt fra signering, og da var du 24 år gammel. Mer info kommer."
	feedInChunks(g, text, 7)
	g.finish()
	if got := g.shownText() + g.heldText(); got != text {
		t.Fatalf("vist+holdt skal være hele svaret, fikk %q", got)
	}
}

func TestMessageTextMultimodal(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "se på dette"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:..."}},
	}
	if got := messageText(content); !strings.Contains(got, "se på dette") {
		t.Fatalf("tekstdel mangler: %q", got)
	}
	if got := messageText("ren streng"); got != "ren streng" {
		t.Fatalf("streng-innhold: %q", got)
	}
}

func TestGroundingBasisCollectsConversationAndTools(t *testing.T) {
	full := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "systeminstruks"},
		map[string]any{"role": "user", "content": "hva er summen?"},
		map[string]any{"role": "assistant", "content": "17 682 ordre."},
	}}
	basis := groundingBasis(full, []string{`{"rows":[["17682"]]}`})
	joined := strings.Join(basis, "\n")
	for _, want := range []string{"systeminstruks", "hva er summen?", "17 682 ordre.", "17682"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("grunnlaget mangler %q: %v", want, basis)
		}
	}
}

// Småtall må treffe et helt tall-token i kildene — aldri substring av et
// større tall («24» i «2024», «99» i «499 000»).
func TestSmallNumberNeverCoveredBySubstring(t *testing.T) {
	src := []string{"Overtakelse 14. juni 2024, pris 499 000 NOK."}
	if off := offendersAgainst("Du var 24 år.", src, 1); len(off) == 0 {
		t.Fatal("24 skulle vært avvik selv om 2024 finnes")
	}
	if off := offendersAgainst("Det skjedde 14. juni.", src, 1); len(off) != 0 {
		t.Fatalf("14 er et ekte token i kilden, fikk avvik %v", off)
	}
}

// Omforsøk etter diktingsdom: sifre inni lange ID-er i verktøydata skal ALDRI
// dekke et tall som ikke står der selv (syntetiske verdier).
func TestStrictOffendersRejectSubstringOfLongIDs(t *testing.T) {
	src := []string{`{"rows":[["order 90102034567890","total 45678.55"]]}`}
	off := strictOffenders("Du har fødselsnummer 010203 45678.", src)
	if len(off) == 0 {
		t.Fatal("tall som bare finnes inni lengre ID-er skulle vært avvik i streng modus")
	}
	// Vanlig modus dekker store tall inni ett kildetall — det er poenget med
	// forskjellen (formateringsvarianter i førstesvar, aldri i omforsøk).
	if off := offendersAgainst("Ordren er 90102034567890.", src, 3); len(off) != 0 {
		t.Fatalf("eksakt kildetall skal passere, fikk %v", off)
	}
}

// «Heller vente»: en setning som ender i en udekket påstand skal ALDRI vises
// halvveis. Før streamet gaten «…versjonen min er fra» og trakk det tilbake
// da datoen avslørte diktingen.
func TestGateHoldsWholeSentenceWithClaim(t *testing.T) {
	g := newSentenceGate([]string{"hvor gammel er du?"}) // datoen står ikke her
	var shown strings.Builder
	// Streamet ord for ord, som en ekte modell.
	for _, chunk := range []string{"Jeg ", "vet ", "ikke, ", "men ", "versjonen ",
		"min ", "er ", "fra ", "2024", "."} {
		shown.WriteString(g.feed(chunk))
	}
	shown.WriteString(g.finish())
	// Første setning («Jeg vet ikke, men…») har ingen påstand og kan vises.
	// Men den udekkede andre-delen med «2024» skal ikke ha lekket.
	if strings.Contains(shown.String(), "versjonen min er fra") {
		t.Fatalf("setningsstart som leder inn i dikting ble vist: %q", shown.String())
	}
	if !g.held {
		t.Fatal("gaten fanget ikke den udekkede datoen")
	}
}

// En KOMPLETT ren setning slippes straks den er ferdig; en ufullstendig
// holdes til punktum (buffer per setning — «heller vente»).
func TestGateReleasesCompletePlainSentence(t *testing.T) {
	g := newSentenceGate(nil)
	if out := g.feed("Dette er et helt vanlig svar uten tall"); out != "" {
		t.Fatalf("ufullstendig setning ble sluppet: %q", out)
	}
	out := g.feed(". Og litt til")
	if !strings.Contains(out, "vanlig svar uten tall.") {
		t.Fatalf("komplett setning ble ikke sluppet: %q", out)
	}
}
