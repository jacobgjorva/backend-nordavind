package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// jsonMarshal gir payloaden som leser for upstream-kall.
func jsonMarshal(v any) (io.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

// Kildekontroll (steg 2 i DIAGNOSE.md): modellens prosa er grensesnittet, men
// den får ikke passere med tall eller egennavn som ikke finnes i det
// verktøyene faktisk returnerte denne turen. Ren tekstsjekk i minnet — null
// nettverkskall, mikrosekunder.

var (
	groundNumRe = regexp.MustCompile(`\d[\d .,]*\d|\d`)
	groundURLRe = regexp.MustCompile(`https?://[^\s)\]"']+`)
	// Egennavn midt i setning: stor forbokstav som ikke følger setningsslutt.
	groundNameRe = regexp.MustCompile(`(^|[^.!?:\n]\s)([A-ZÆØÅ][a-zæøåA-ZÆØÅ]+(?:\s+[A-ZÆØÅ][a-zæøåA-ZÆØÅ]+)*)`)
)

// normDigits reduserer et tall til ren siffersekvens («17 682» → «17682»,
// «3.695,52» → «369552») så formateringsvalg aldri gir falske avvik.
func normDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// groundingOffenders returnerer tall og egennavn i prosaen som IKKE kan
// spores til kildematerialet. Tom liste = svaret er kildefast.
func groundingOffenders(prose string, sources []string) []string {
	return offendersAgainst(prose, sources, 3)
}

// strictOffenders brukes på OMFORSØK (etter en diktingsdom): hvert tall må
// treffe et helt kildetall eksakt. Substring-dekningen er avskrudd — et diktet
// fødselsnummer ble «dekket» av sifre inni lange ID-er i verktøydataene.
func strictOffenders(prose string, sources []string) []string {
	return offendersCore(prose, sources, 1, true)
}

// offendersAgainst er kjernen: minDigits styrer hvor små tall som sjekkes.
// Verktøy-turer bruker 3 (telling/formulering slipper), setnings-gaten bruker
// 1 (en diktet alder er to sifre — alt må kunne spores eller dømmes).
func offendersAgainst(prose string, sources []string, minDigits int) []string {
	return offendersCore(prose, sources, minDigits, false)
}

func offendersCore(prose string, sources []string, minDigits int, exactNums bool) []string {
	if strings.TrimSpace(prose) == "" || len(sources) == 0 {
		return nil
	}
	src := strings.ToLower(strings.Join(sources, "\n"))
	// Tall-tokens i kildene, normalisert: dekning krever treff i ETT kildetall
	// — «24» dekkes ALDRI av at «2024» finnes (substring-fella), og den
	// sammenklistrede sifferstrengen av HELE kildematerialet dekker ingenting.
	srcNums := map[string]bool{}
	var srcNumList []string
	for _, m := range groundNumRe.FindAllString(src, -1) {
		d := normDigits(m)
		if !srcNums[d] {
			srcNums[d] = true
			srcNumList = append(srcNumList, d)
		}
	}

	var offenders []string
	seen := map[string]bool{}

	for _, m := range groundNumRe.FindAllString(prose, -1) {
		d := normDigits(m)
		if len(d) < minDigits || seen[d] {
			continue
		}
		seen[d] = true
		// Store tall (3+ sifre) dekkes også av å ligge INNI ett enkelt
		// kildetall (formateringsvarianter); småtall kun eksakt token-treff.
		// Aldri råtekst-substring: «24» skal ikke dekkes av teksten «2024».
		covered := srcNums[d]
		if !covered && !exactNums && len(d) >= 3 {
			for _, t := range srcNumList {
				if strings.Contains(t, d) {
					covered = true
					break
				}
			}
		}
		if !covered {
			offenders = append(offenders, strings.TrimSpace(m))
		}
	}

	// Lenker: en URL som ikke finnes i kildene er alltid dikt — hard blokk.
	for _, u := range groundURLRe.FindAllString(prose, -1) {
		lu := strings.ToLower(strings.TrimRight(u, "./"))
		if seen[lu] {
			continue
		}
		seen[lu] = true
		if !strings.Contains(src, lu) {
			offenders = append(offenders, u)
		}
	}

	for _, m := range groundNameRe.FindAllStringSubmatch(prose, -1) {
		name := strings.TrimSpace(m[2])
		// Enkeltord med stor bokstav er ofte vanlige ord etter komma o.l. —
		// kun flerords-navn (Pernod Ricard, Cask AS) sjekkes strengt.
		if !strings.Contains(name, " ") || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		if !strings.Contains(src, strings.ToLower(name)) {
			offenders = append(offenders, name)
		}
	}
	return offenders
}

// messageText henter ren tekst fra en meldings content — enten en streng
// eller multimodale deler ({"type":"text","text":…}).
func messageText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
					b.WriteString("\n")
				}
			}
		}
		return b.String()
	}
	return ""
}

// groundingBasis er alt et svar lovlig kan bygge på denne turen: hele
// samtalen (brukerens utsagn, vedlagte dokumenter, tidligere svar) pluss det
// verktøyene har returnert. Fabrikkert innhold fanges FØR det vises — når det
// først står i historikken, er det en del av samtalen.
func groundingBasis(full map[string]any, toolResults []string) []string {
	var basis []string
	if msgs, ok := full["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if t := messageText(mm["content"]); strings.TrimSpace(t) != "" {
					basis = append(basis, t)
				}
			}
		}
	}
	return append(basis, toolResults...)
}

// sentenceGate slipper et streamet svar gjennom ord for ord, men holder igjen
// fra første mulige faktapåstand (tall, URL, egennavn midt i setning) til
// setningen er komplett og sjekket mot grunnlaget. Ren tekst i minnet —
// setninger uten slike kandidater får null ekstra ventetid.
type sentenceGate struct {
	sources   []string
	buf       string          // ennå ikke sluppet
	shown     strings.Builder // alt brukeren har sett
	prev      rune            // siste ikke-blanke tegn som er sluppet (setningskontekst)
	held      bool            // udekket påstand funnet — alt videre holdes igjen
	offenders []string
}

func newSentenceGate(sources []string) *sentenceGate {
	return &sentenceGate{sources: sources}
}

// feed tar imot neste bit av streamen og returnerer det som trygt kan vises nå.
func (g *sentenceGate) feed(chunk string) string {
	g.buf += chunk
	if g.held {
		return ""
	}
	var out strings.Builder
	for {
		ci := g.candidateIdx()
		if ci < 0 {
			// Ingen kandidat: slipp alt unntatt en hale som kan bli «http».
			keep := partialSuffix(strings.ToLower(g.buf), "http")
			g.release(g.buf[:len(g.buf)-keep], &out)
			g.buf = g.buf[len(g.buf)-keep:]
			return out.String()
		}
		g.release(g.buf[:ci], &out)
		g.buf = g.buf[ci:]
		end := sentenceEnd(g.buf)
		if end < 0 {
			// Setningen med kandidaten er ikke ferdig — vent på mer.
			return out.String()
		}
		sent := g.buf[:end]
		if off := offendersAgainst(sent, g.sources, 1); len(off) > 0 {
			g.held = true
			g.offenders = off
			return out.String()
		}
		g.release(sent, &out)
		g.buf = g.buf[end:]
	}
}

// finish sjekker halen når streamen er ferdig. Returnerer det som kan vises;
// er halen udekket settes held i stedet.
func (g *sentenceGate) finish() string {
	if g.held || g.buf == "" {
		return ""
	}
	if off := offendersAgainst(g.buf, g.sources, 1); len(off) > 0 {
		g.held = true
		g.offenders = off
		return ""
	}
	var out strings.Builder
	g.release(g.buf, &out)
	g.buf = ""
	return out.String()
}

// heldText er det tilbakeholdte (aldri viste) innholdet.
func (g *sentenceGate) heldText() string { return g.buf }

// shownText er prefikset brukeren allerede har sett.
func (g *sentenceGate) shownText() string { return g.shown.String() }

func (g *sentenceGate) release(s string, out *strings.Builder) {
	if s == "" {
		return
	}
	out.WriteString(s)
	g.shown.WriteString(s)
	for _, r := range s {
		if !isSpaceRune(r) {
			g.prev = r
		}
	}
}

func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// candidateIdx finner første posisjon i bufferet som KAN starte en
// faktapåstand: et siffer, en URL, eller stor bokstav midt i en setning.
func (g *sentenceGate) candidateIdx() int {
	prev := g.prev
	for i, r := range g.buf {
		switch {
		case r >= '0' && r <= '9':
			return i
		case r == 'h' || r == 'H':
			if strings.HasPrefix(strings.ToLower(g.buf[i:]), "http") {
				return i
			}
		case (r >= 'A' && r <= 'Z') || r == 'Æ' || r == 'Ø' || r == 'Å':
			// Stor bokstav som ikke åpner en setning = mulig egennavn.
			if prev != 0 && !strings.ContainsRune(".!?:…\n", prev) {
				return i
			}
		}
		if !isSpaceRune(r) {
			prev = r
		}
	}
	return -1
}

// sentenceEnd gir byteposisjonen rett etter setningsslutt, eller -1 om
// setningen ikke er komplett ennå. Punktum midt i tall («3.695») og ordenstall
// uten mellomrom etter avslutter ikke.
func sentenceEnd(s string) int {
	for i, r := range s {
		if r == '\n' {
			return i + 1
		}
		if r == '.' || r == '!' || r == '?' || r == '…' {
			rest := s[i+len(string(r)):]
			if rest == "" {
				return -1 // vet ikke før neste tegn kommer
			}
			if nr, _ := utf8.DecodeRuneInString(rest); unicode.IsSpace(nr) {
				return i + len(string(r))
			}
		}
	}
	return -1
}

// regroundAnswer gir modellen ETT bufret omforsøk med eksplisitt beskjed om
// hvilke verdier som ikke fantes i kildene. Returnerer kildefast svar, eller
// "" hvis også omforsøket avvek.
func (s *Server) regroundAnswer(ctx context.Context, full map[string]any, draft string, offenders, toolResults []string, promptTokens, completionTokens *int) string {
	fixed := map[string]any{}
	for k, v := range full {
		fixed[k] = v
	}
	msgs, _ := full["messages"].([]any)
	msgs = append(append([]any{}, msgs...),
		map[string]any{"role": "assistant", "content": draft},
		map[string]any{"role": "user", "content": "RETTELSE: svaret ditt inneholdt verdier som IKKE finnes i verktøydataene: " +
			strings.Join(offenders, ", ") + ". Skriv svaret på nytt og bruk KUN tall og navn som står ordrett i verktøyresultatene."})
	fixed["messages"] = msgs
	delete(fixed, "tools")

	body, err := jsonMarshal(fixed)
	if err != nil {
		return ""
	}
	req, err := s.newUpstreamRequest(ctx, body)
	if err != nil {
		return ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, usage, content := s.relayRound(resp, func(string) {}, false, nil)
	*promptTokens += usage.PromptTokens
	*completionTokens += usage.CompletionTokens
	content = strings.TrimSpace(content)
	if content == "" || len(strictOffenders(content, toolResults)) > 0 {
		return ""
	}
	return content
}

// regroundContinuation brukes når deler av et streamet svar allerede er vist
// og resten ble underkjent: modellen får ETT bufret forsøk på å fortsette
// NATURLIG fra det viste, uten de udekkede påstandene. Returnerer
// fortsettelsen, eller "" hvis også den avvek fra grunnlaget.
func (s *Server) regroundContinuation(ctx context.Context, full map[string]any, draft, shown string, offenders, basis []string, promptTokens, completionTokens *int) string {
	tail := shown
	if r := []rune(tail); len(r) > 200 {
		tail = string(r[len(r)-200:])
	}
	fixed := map[string]any{}
	for k, v := range full {
		fixed[k] = v
	}
	msgs, _ := full["messages"].([]any)
	msgs = append(append([]any{}, msgs...),
		map[string]any{"role": "assistant", "content": draft},
		map[string]any{"role": "user", "content": "RETTELSE: svaret ditt inneholdt påstander uten dekning i samtalen eller kildene: " +
			strings.Join(offenders, ", ") + ". Brukeren har allerede sett svaret frem til og med «" + tail +
			"». Fortsett NATURLIG akkurat derfra (ikke gjenta noe av det viste), uten de udekkede påstandene. " +
			"Mangler du grunnlag for noe, si ærlig at du ikke vet."})
	fixed["messages"] = msgs
	delete(fixed, "tools")

	body, err := jsonMarshal(fixed)
	if err != nil {
		return ""
	}
	req, err := s.newUpstreamRequest(ctx, body)
	if err != nil {
		return ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, usage, content := s.relayRound(resp, func(string) {}, false, nil)
	*promptTokens += usage.PromptTokens
	*completionTokens += usage.CompletionTokens
	content = strings.TrimRight(content, " \n")
	if strings.TrimSpace(content) == "" || len(strictOffenders(content, basis)) > 0 {
		return ""
	}
	if shown != "" && !strings.HasSuffix(shown, " ") && !strings.HasPrefix(content, " ") &&
		!strings.ContainsRune(".,!?;:—-", []rune(content)[0]) {
		content = " " + content
	}
	return content
}

// midSentence: det viste prefikset slutter midt i en setning (eller er tomt
// prat frem mot en påstand) — en modell-fortsettelse leser da dårlig.
func midSentence(shown string) bool {
	t := strings.TrimSpace(shown)
	if t == "" {
		return false
	}
	r := []rune(t)
	return !strings.ContainsRune(".!?…", r[len(r)-1])
}

// honestCut er den ærlige nødutgangen når heller ikke fortsettelsen holdt:
// klipp der det viste slapp og si det som det er.
func honestCut(shown string) string {
	t := strings.TrimSpace(shown)
	if t == "" {
		return "Det har jeg ikke grunnlag for å svare på ut fra det jeg har her."
	}
	if strings.ContainsRune(".!?…", []rune(t)[len([]rune(t))-1]) {
		return " Det jeg skulle til å legge til hadde jeg ikke grunnlag for, så det dropper jeg."
	}
	// Midt i en setning: det viste fragmentet kan ikke tas tilbake (det er
	// alt streamet), så halen må gjøre linja forståelig på egen hånd — «Årets
	// ord var " — nei, vent: …» leste som en feil, ikke som et ærlig svar.
	return "… – her stopper jeg: resten hadde jeg ikke dekning for i kildene. " +
		"Se bort fra den halve setningen over."
}

// hasNameOffender: minst ett avvik er et egennavn eller en lenke (ikke tall).
func hasNameOffender(offenders []string) bool {
	for _, o := range offenders {
		if strings.HasPrefix(o, "http://") || strings.HasPrefix(o, "https://") {
			return true
		}
		if normDigits(o) == "" || len(normDigits(o)) < len(strings.ReplaceAll(o, " ", ""))/2 {
			if regexp.MustCompile(`[A-ZÆØÅa-zæøå]{2,}`).MatchString(o) {
				return true
			}
		}
	}
	return false
}
