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
	"ordrett i grunnlaget. Vurder hver flagget verdi: (a) grei beregning, telling, omformulering av noe " +
	"i grunnlaget, eller ukontroversiell allmennkunnskap (etablerte begreper og navn) — eller (b) en " +
	"påstand uten dekning: dikting. Avrunding, formatering, summering og differanser av tall som står " +
	"i grunnlaget er ALLTID (a) — aldri flagg et tall fordi det er avrundet eller skrevet annerledes. Fakta om brukeren selv (alder, fødselsår, navn, relasjoner) eller om " +
	"brukerens virksomhet og data er ALLTID (b) når de ikke står i grunnlaget — å «anslå» slikt er aldri " +
	"lov. Svar KUN med JSON: {\"ok\":true/false,\"problemer\":[\"kort beskrivelse per udekket påstand\"]}"

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
	var v struct {
		OK        bool     `json:"ok"`
		Problemer []string `json:"problemer"`
	}
	if json.Unmarshal([]byte(raw[start:end+1]), &v) != nil {
		return true, nil
	}
	return v.OK, v.Problemer
}
