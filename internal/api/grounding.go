package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
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
	if strings.TrimSpace(prose) == "" || len(sources) == 0 {
		return nil
	}
	src := strings.ToLower(strings.Join(sources, "\n"))
	srcDigits := normDigits(src)

	var offenders []string
	seen := map[string]bool{}

	for _, m := range groundNumRe.FindAllString(prose, -1) {
		d := normDigits(m)
		// Småtall (én-to sifre) er oftest telling/formulering («3 kilder»,
		// «2 av dem») — de sjekkes ikke.
		if len(d) < 3 || seen[d] {
			continue
		}
		seen[d] = true
		if !strings.Contains(srcDigits, d) && !strings.Contains(src, d) {
			offenders = append(offenders, strings.TrimSpace(m))
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
	_, usage, content := s.relayRound(resp, func(string) {}, false)
	*promptTokens += usage.PromptTokens
	*completionTokens += usage.CompletionTokens
	content = strings.TrimSpace(dedupeStutter(content))
	if content == "" || len(groundingOffenders(content, toolResults)) > 0 {
		return ""
	}
	return content
}
