package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

// Leveransens api-side: skriveren og tallkontrollen.
//
// Begge er tynne adaptere over kode som allerede finnes og er målt —
// upstream-kallet i server.go og kildekontrollen i grounding.go. Ingen ny
// vurderingslogikk her.

// motorWriter skriver sluttsvaret når utkastet ikke holder. ETT kall med
// kompakt kontekst: persona + samtale + fakta. Aldri hele historikken på
// nytt — den var den største enkeltposten i tokenregnskapet.
type motorWriter struct {
	s    *Server
	full map[string]any
	// promptTokens/completionTokens føres på turens regnskap.
	promptTokens, completionTokens *int
}

func (w *motorWriter) Write(ctx context.Context, turn *motor.Turn, facts, draft string) string {
	fixed := map[string]any{}
	for k, v := range w.full {
		fixed[k] = v
	}

	// Persona beholdes; alt annet erstattes av et utdrag.
	var sysText string
	if msgs, ok := w.full["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
				sysText = messageText(mm["content"])
				break
			}
		}
	}

	// Ingen verktøy i skriverunden — og modellen får det SAGT. Uten
	// setningen skrev den verktøykall som ren tekst når den «ville» søke
	// mer (målt i prod 2026-07-30: «web\_search {…}» rett i svaret,
	// markdown-escapet forbi vaktene).
	instr := "Skriv sluttsvaret til brukeren nå. Du har INGEN verktøy i denne runden: skriv aldri " +
		"verktøynavn, kall eller kodeblokker — mangler du noe, si heller ærlig hva du ikke fikk sjekket."
	if facts != "" {
		instr = "Koden har regnet dette av dataene dine — bruk tallene ordrett:\n" + facts +
			"\n\nSkriv sluttsvaret til brukeren nå, med konklusjonen tallfestet. Du har INGEN verktøy " +
			"i denne runden: skriv aldri verktøynavn eller kall."
	}

	compact := []any{}
	if sysText != "" {
		compact = append(compact, map[string]any{"role": "system", "content": sysText})
	}
	compact = append(compact,
		map[string]any{"role": "user", "content": "SAMTALEN:\n" + recentTurn(w.full) +
			"\n\nSPØRSMÅL: " + turn.Question},
		map[string]any{"role": "user", "content": instr})
	fixed["messages"] = compact
	// Uten verktøy i denne runden: svaret skal skrives, ikke hentes på nytt.
	disableTools(fixed)

	body, err := json.Marshal(fixed)
	if err != nil {
		return draft
	}
	req, err := w.s.newUpstreamRequest(ctx, strings.NewReader(string(body)))
	if err != nil {
		return draft
	}
	resp, err := w.s.client.Do(req)
	if err != nil {
		return draft
	}
	defer resp.Body.Close()
	_, usage, content := w.s.relayRound(resp, func(string) {}, false, nil)
	if w.promptTokens != nil {
		*w.promptTokens += usage.PromptTokens
	}
	if w.completionTokens != nil {
		*w.completionTokens += usage.CompletionTokens
	}
	turn.Usage.PromptTokens += usage.PromptTokens
	turn.Usage.CompletionTokens += usage.CompletionTokens

	if c := strings.TrimSpace(content); c != "" {
		return c
	}
	return draft
}

// motorVerifier er kildekontrollen fra grounding.go, brukt som TELEMETRI.
//
// KUN TALL. offendersAgainst sjekker både tall og egennavn, men navnedelen
// er ubrukelig som telemetri uten en dommer bak: målt på ti ekte,
// manuelt kontrollerte svar slo den ut på syv — «Andre aktører»,
// «Anbefaling», «Prisen», «UX-konsulentbyrå». Norsk har stor forbokstav
// midt i setninger av mange grunner, og legacy løste det med et
// dommer-kall (judgeClaims). v6 har ingen dommer med vilje, så
// navnekontroll ville bare fylt loggen med støy ingen leser.
//
// Et tall er derimot etterprøvbart uten skjønn: enten står sifrene i
// kildene, eller de gjør det ikke.
//
// minDigits=3: telling og formuleringer («én av tre», «to ganger») skal
// aldri havne i logg som dikting. Samme terskel som legacy bruker på
// verktøyturer.
//
// Svaret strippes først: uten det blir markørene en del av tokenet
// («DA** Regnskapsbyrå**») og kontrollen måler sin egen formatering.
type motorVerifier struct{}

// pureNumberRe: tokenet ER et tall — sifre med vanlige skilletegn. Skiller
// «0,50» fra «BM25»: produktnavn med sifre i seg (BM25, M365, x86) er NAVN,
// og navnekontroll er bevisst fravalgt. Målt i prod 2026-07-30: «BM25»
// havnet i avvikslista og kunne endt i en «les som anslag»-merknad.
var pureNumberRe = regexp.MustCompile(`^[0-9][0-9 .,:%–-]*$`)

func (motorVerifier) Unsupported(answer string, basis []string) []string {
	var nums []string
	for _, o := range offendersAgainst(motor.StripEmphasis(answer), basis, 3) {
		o = strings.TrimSpace(o)
		if !pureNumberRe.MatchString(o) {
			continue
		}
		d := normDigits(o)
		if d == "" {
			continue
		}
		// Årstall er ikke dikting: «siden 2009» og «i 2026» er kontekst,
		// ikke påstander brukeren tar beslutninger på. Målt logikk fra
		// engine-experiment (realOffenders) — uten filteret ble korrekte
		// svar felt på årstall som ikke sto ordrett i kildene.
		if len(d) == 4 && (strings.HasPrefix(d, "19") || strings.HasPrefix(d, "20")) {
			continue
		}
		nums = append(nums, o)
	}
	return nums
}

// motorForm er formgulvet: legacys målte isJunkAnswer (sse.go), uendret.
type motorForm struct{}

func (motorForm) IsJunk(text string) bool { return isJunkAnswer(text) }

// motorFacts regner det koden kan regne selv av turens data.
//
// IKKE PORTERT ENNÅ: seriestatistikk og Pearson-korrelasjon finnes bare på
// engine-experiment. Å kopiere umålt kode hit ville brutt regelen om at
// gulv skal være verifiserte, så laget returnerer tom streng — leveransen
// hopper da over omskrivingen, nøyaktig som når det ikke er noe å regne.
type motorFacts struct{}

func (motorFacts) Facts(*motor.Turn) string { return "" }

// motorCompress er lengdegulvets modellkall — samme prompt som proben
// (kjøring 15) validerte på ekte lange svar.
type motorCompress struct {
	s                              *Server
	promptTokens, completionTokens *int
}

const compressInstr = `Komprimer teksten under til godt under %d tegn, på norsk.
UFRAVIKELIG: behold HVERT tall, HVERT navn og HVERT faktum ordrett — kutt kun fyll, gjentakelser og omskrivinger.
Behold standpunktet/anbefalingen med begrunnelse, en eventuell avgrensning mot det som IKKE passer, og et eventuelt avsluttende spørsmål.
Løpende tekst, ingen lister. Svar KUN med den komprimerte teksten.

TEKST:
%s`

func (c *motorCompress) Compress(ctx context.Context, answer string, limit int) string {
	body, err := json.Marshal(map[string]any{
		"model": router.MidModel, "stream": false, "temperature": 0.2, "max_tokens": 900,
		"messages": []any{map[string]any{"role": "user",
			"content": fmt.Sprintf(compressInstr, limit, answer)}},
	})
	if err != nil {
		return ""
	}
	req, err := c.s.newUpstreamRequest(ctx, strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	resp, err := c.s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, usage, content := c.s.relayRound(resp, func(string) {}, false, nil)
	if c.promptTokens != nil {
		*c.promptTokens += usage.PromptTokens
	}
	if c.completionTokens != nil {
		*c.completionTokens += usage.CompletionTokens
	}
	return strings.TrimSpace(content)
}
