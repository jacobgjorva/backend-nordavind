package api

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/router"
)

// forceSearch: eksplisitte søkeønsker og entitetsoppslag går alltid til søk,
// uavhengig av hva flash-dommeren mener.
var forceSearch = regexp.MustCompile(`(?i)(søk|slå opp|sjekk nettet|hvem er|hva er .+\b(as|asa|ab|ltd|gmbh|inc)\b)`)

// maybeSearch avgjør med et raskt flash-kall om spørsmålet trenger ferske
// data. Hvis ja: kjør websøk og returner kildekontekst som injiseres i
// samtalen. Returnerer "" hvis søk ikke trengs eller feiler.
func (s *Server) maybeSearch(ctx context.Context, messages []router.Message) string {
	if s.search == nil || !s.search.Enabled() {
		return ""
	}
	// Siste + nest siste brukermelding gir kontekst for korte oppfølginger
	// som "søk på nettet".
	var userMsgs []string
	for i := len(messages) - 1; i >= 0 && len(userMsgs) < 2; i-- {
		if messages[i].Role == "user" {
			userMsgs = append([]string{messages[i].Content}, userMsgs...)
		}
	}
	if len(userMsgs) == 0 {
		return ""
	}
	last := userMsgs[len(userMsgs)-1]
	combined := strings.Join(userMsgs, "\n")

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	// Flash formulerer alltid en selvstendig søkestreng med samtalekontekst.
	// Ved tvungne mønstre (eksplisitt søk / entitetsoppslag) brukes siste
	// melding som fallback hvis flash svarer NEI.
	query := s.decideSearchQuery(ctx, combined)
	if query == "" && forceSearch.MatchString(last) {
		query = strings.TrimSpace(combined)
		if r := []rune(query); len(r) > 100 {
			query = string(r[:100])
		}
	}
	if query == "" {
		return ""
	}

	results, err := s.search.Search(ctx, query)
	if err != nil || len(results) == 0 {
		s.log.Warn("websøk feilet", "err", err)
		return ""
	}
	// Hent innholdet fra topptreffene — snippets alene er for tynt.
	if len(results) > 3 {
		results = results[:3]
	}
	pages := s.search.FetchPages(ctx, results, 2000)
	s.log.Info("websøk", "query", query, "treff", len(results))
	return formatSearchContext(query, results, pages)
}

// decideSearchQuery spør flash-modellen om spørsmålet krever oppdatert
// informasjon. Svarer med søkestreng, eller "NEI".
func (s *Server) decideSearchQuery(ctx context.Context, question string) string {
	payload := map[string]any{
		"model": router.LightModel,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": "Du får de siste brukermeldingene i en samtale. Avgjør om SISTE melding krever " +
					"faktaoppslag: fersk informasjon (nyheter, priser, hendelser, 'i dag/nå/siste') ELLER " +
					"spesifikke entiteter som bedrifter, personer, produkter eller steder. Hvis ja: svar KUN " +
					"med en kort SELVSTENDIG søkestreng — oppfølgingsspørsmål skal inkludere konteksten fra " +
					"tidligere meldinger (f.eks. 'hvem er daglig leder?' etter et spørsmål om et selskap blir " +
					"'daglig leder <selskapsnavn>'). Hvis nei (allmennkunnskap, matte, koding): svar KUN 'NEI'.",
			},
			{"role": "user", "content": question},
		},
		"max_tokens":  40,
		"temperature": 0,
		"reasoning":   map[string]any{"enabled": false},
		"provider":    map[string]any{"sort": "throughput"},
	}
	body, _ := json.Marshal(payload)

	req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
	if err != nil {
		return ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return ""
	}
	answer := strings.TrimSpace(out.Choices[0].Message.Content)
	if answer == "" || strings.EqualFold(answer, "NEI") || len(answer) > 120 {
		return ""
	}
	return strings.Trim(answer, `"`)
}
