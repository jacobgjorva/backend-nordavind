package api

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

// Arbeids-dommer for operasjonelt viktige oppgaver: før svaret vises,
// dobbeltsjekker en egen dommer (Storm-modellen) at arbeidet stemmer med
// verktøydataene og oppgaven. Utløses deterministisk av viktighetssignaler
// i meldingen, og vises med egen status i chatten.

// importantWorkRe: oppgaver der en feil koster penger eller tillit.
var importantWorkRe = regexp.MustCompile(`(?i)viktig|kritisk|dobbeltsjekk|faktura|kontrakt|avtale|tilbud|rabatt|styre|regnskap|lønn|purring|reklamasjon|betaling`)

const workJudgeSystem = "Du er kvalitetskontrollør. Du får BRUKERENS OPPGAVE, VERKTØYDATA (fasit) og SVARET. " +
	"Sjekk: (1) svarer det på hele oppgaven, (2) stemmer alle tall/navn/datoer med verktøydataene, " +
	"(3) er noe utelatt som oppgaven ba om. Svar KUN med JSON: " +
	"{\"ok\":true/false,\"problemer\":[\"kort beskrivelse per problem\"]}"

// verifyWork lar dommeren vurdere svaret. ok=true betyr godkjent; ellers
// returneres problemlisten (tom liste ved dommer-feil = slipp gjennom).
func (s *Server) verifyWork(ctx context.Context, task, answer string, toolResults []string) (bool, []string) {
	src := strings.Join(toolResults, "\n---\n")
	if len([]rune(src)) > 8000 {
		src = string([]rune(src)[:8000]) + " …"
	}
	user := "OPPGAVE:\n" + task + "\n\nVERKTØYDATA:\n" + src + "\n\nSVARET:\n" + answer
	body, _ := json.Marshal(map[string]any{
		"model": router.HeavyModel,
		"messages": []any{
			map[string]any{"role": "system", "content": workJudgeSystem},
			map[string]any{"role": "user", "content": user},
		},
		"stream": false, "temperature": 0, "max_tokens": 300,
		"reasoning": map[string]any{"enabled": false},
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
	s.countLLM(ctx, router.HeavyModel, "workjudge", out.Usage.PromptTokens, out.Usage.CompletionTokens)
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
