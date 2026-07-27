package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// SearXNG: self-hostet metasøk (EU, ingen API-nøkkel) — primærkilden for
// websøk. Motorspredning og vekting konfigureres i deploy/searxng/settings.yml;
// denne klienten trenger bare JSON-API-et. NB: instansen MÅ ha `json` i
// search.formats, ellers svarer den 403.

// searxSearch spør SearXNG-instansen og returnerer treff + navnene på motorer
// som ikke svarte (helsemetrikk for blokkeringsovervåking).
func (c *Client) searxSearch(ctx context.Context, query string) ([]Result, []string, error) {
	// language=all: spørringens EGET språk styrer treffene. Et påtvunget
	// nb-NO-filter ga ingen gevinst, bare støy — målt: på engelske spørringer
	// blandet det inn irrelevante norske sider («Exams and AI – Kunnskapsbasen»)
	// og skjulte de faktiske kildene, mens norske spørringer (styringsrenten,
	// arbeidsmiljøloven, Språkrådet) traff like godt eller BEDRE uten det.
	// Modellen velger allerede språk når den skriver søkestrengen; det er
	// signalet vi skal følge, ikke overstyre.
	u := c.searxURL + "/search?q=" + url.QueryEscape(query) +
		"&format=json&language=all&safesearch=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("searxng: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, nil, err
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
		// [[motornavn, feiltype], …] — motorer som feilet/ble blokkert.
		Unresponsive [][]any `json:"unresponsive_engines"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil, fmt.Errorf("searxng: ugyldig JSON: %w", err)
	}

	var results []Result
	for _, r := range out.Results {
		if r.URL == "" {
			continue
		}
		results = append(results, Result{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Content,
		})
		if len(results) == 6 {
			break
		}
	}
	var down []string
	for _, e := range out.Unresponsive {
		if len(e) > 0 {
			if name, ok := e[0].(string); ok {
				down = append(down, name)
			}
		}
	}
	return results, down, nil
}
