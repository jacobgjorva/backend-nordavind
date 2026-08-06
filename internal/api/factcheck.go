package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

// Faktadommer (G2 i DIAGNOSE.md): når et svar inneholder verdier som ikke kan
// spores i grunnlaget (samtale + dokumenter + verktøydata), avgjør en egen
// dommer om det er lovlig beregning/omformulering — eller dikting. Modellen
// skal aldri «vite» noe om brukeren eller brukerens virksomhet som ikke står
// i grunnlaget.

const factJudgeSystem = "Du er faktakontrollør for en samtaleassistent. GRUNNLAGET er alt assistenten " +
	"lovlig kan bygge på: brukerens meldinger, vedlagte dokumenter og verktøydata denne samtalen. " +
	"Du får også BRUKERENS SPØRSMÅL, assistentens SVARUTKAST og FLAGGEDE VERDIER som ikke ble funnet " +
	"ordrett i grunnlaget. Du dømmer KUN de flaggede verdiene — én dom per verdi, aldri noe annet i svaret. " +
	"For hver flagget verdi: ok=true hvis den er grei beregning, telling, avrunding, omformulering av noe " +
	"i grunnlaget, eller ukontroversiell allmennkunnskap (etablerte begreper og navn); ok=false kun hvis " +
	"verdien er en faktapåstand UTEN dekning: dikting. Avrunding, formatering, summering og differanser av " +
	"tall som står i grunnlaget er ALLTID ok=true — aldri underkjenn et tall fordi det er avrundet eller " +
	"skrevet annerledes. Fakta om brukeren selv (alder, fødselsår, navn, relasjoner) eller om brukerens " +
	"virksomhet og data er ALLTID ok=false når de ikke står i grunnlaget — å «anslå» slikt er aldri lov. " +
	// Drive-stilen (2026-08-06): assistenten SKAL ta stilling. Dommeren
	// underkjente råd og hypoteser som «uten belegg», og svaret falt til naken
	// tabell — målt i drive-eval. Vurderinger er ikke faktapåstander.
	"VIKTIG: assistenten SKAL vurdere, anbefale, prioritere og foreslå. Vurderinger, råd, hypoteser, " +
	"tilbud om videre arbeid og verdiord («lavt», «svakt», «bør», «neppe verdt») er IKKE faktapåstander — " +
	"de skal ALDRI underkjennes, heller ikke når begrunnelsen går ut over grunnlaget. Du vurderer bare om " +
	"selve TALLET eller NAVNET har dekning. " +
	"Svar KUN med JSON: {\"dommer\":[{\"verdi\":\"<den flaggede verdien ordrett>\",\"ok\":true/false," +
	"\"grunn\":\"kort, kun ved ok=false\"}]}"

// judgeClaims: ok=true betyr at alle flaggede verdier har dekning (eller er
// lovlige beregninger). Dommer-feil = slipp gjennom (fail-open som ellers).
func (s *Server) judgeClaims(ctx context.Context, question, answer string, offenders, basis []string) (bool, []string) {
	src := strings.Join(basis, "\n---\n")
	if r := []rune(src); len(r) > 8000 {
		// Behold begge ender: dokumenter ligger tidlig, ferske utsagn sist.
		src = string(r[:4000]) + "\n…\n" + string(r[len(r)-4000:])
	}
	user := "GRUNNLAGET:\n" + src + "\n\nBRUKERENS SPØRSMÅL:\n" + question +
		"\n\nSVARUTKAST:\n" + answer + "\n\nFLAGGEDE VERDIER:\n" + strings.Join(offenders, ", ")
	body, _ := json.Marshal(map[string]any{
		"model": router.MidModel,
		"messages": []any{
			map[string]any{"role": "system", "content": factJudgeSystem},
			map[string]any{"role": "user", "content": user},
		},
		"stream": false, "temperature": 0, "max_tokens": 300,
	})
	req, err := s.newUpstreamRequest(ctx, strings.NewReader(string(body)))
	if err != nil {
		return true, nil
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return true, nil
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	s.countLLM(ctx, router.MidModel, "faktadommer", out.Usage.PromptTokens, out.Usage.CompletionTokens)
	if decodeErr != nil || len(out.Choices) == 0 {
		return true, nil
	}
	raw := out.Choices[0].Message.Content
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return true, nil
	}
	return parseFactVerdicts(raw[start:end+1], offenders)
}

// parseFactVerdicts leser dommen og HÅNDHEVER mandatet i kode: bare dommer som
// peker på en faktisk flagget verdi teller. Dommeren gled tidligere ut i å
// underkjenne anbefalinger og premisser («minstesum på 500 kroner mangler
// belegg»), og et helt gyldig svar med vurdering falt til naken tabell.
// Uparsbart eller tomt = slipp gjennom (fail-open som ellers).
func parseFactVerdicts(jsonBody string, offenders []string) (bool, []string) {
	var v struct {
		Dommer []struct {
			Verdi string `json:"verdi"`
			OK    bool   `json:"ok"`
			Grunn string `json:"grunn"`
		} `json:"dommer"`
		// Bakoverkompatibelt: eldre dommer-svar (ok + problemer).
		OK        *bool    `json:"ok"`
		Problemer []string `json:"problemer"`
	}
	if json.Unmarshal([]byte(jsonBody), &v) != nil {
		return true, nil
	}
	if len(v.Dommer) == 0 {
		if v.OK != nil && !*v.OK && len(v.Problemer) > 0 {
			// Gammelt format: behold bare problemer som nevner en flagget verdi.
			kept := filterByOffenders(v.Problemer, offenders)
			return len(kept) == 0, kept
		}
		return true, nil
	}
	var problems []string
	for _, d := range v.Dommer {
		if d.OK || !mentionsOffender(d.Verdi, offenders) {
			continue
		}
		msg := strings.TrimSpace(d.Verdi)
		if g := strings.TrimSpace(d.Grunn); g != "" {
			msg += ": " + g
		}
		problems = append(problems, msg)
	}
	return len(problems) == 0, problems
}

// mentionsOffender: gjelder teksten en av verdiene kildekontrollen faktisk
// flagget? Sammenlignes normalisert, så «453 000» og «453000» er samme verdi.
func mentionsOffender(text string, offenders []string) bool {
	low := strings.ToLower(text)
	lowDigits := normDigits(text)
	for _, o := range offenders {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(o)) {
			return true
		}
		if d := normDigits(o); d != "" && lowDigits != "" && strings.Contains(lowDigits, d) {
			return true
		}
	}
	return false
}

func filterByOffenders(problems, offenders []string) []string {
	var kept []string
	for _, p := range problems {
		if mentionsOffender(p, offenders) {
			kept = append(kept, p)
		}
	}
	return kept
}
