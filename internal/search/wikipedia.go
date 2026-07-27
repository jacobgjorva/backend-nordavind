package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Wikipedia som ALLTID tilgjengelig kilde ved siden av søkemotorene.
//
// Bakgrunn (målt fra prod 2026-07-27): datasenter-IP-en vår avvises av google,
// startpage, brave, DDG, qwant og yahoo — ikke på volum, men fordi de nekter
// datasenter-IP-er. Mojeek er alt som svarer, og den rate-limiter etter ~10
// raske søk; 7 av 16 spørringer ga null treff. Wikipedia blokkerer oss ikke,
// og dekker en stor del av oppslagsspørsmålene brukerne faktisk stiller.
//
// Treffene legges inn som vanlige Result-er og går gjennom nøyaktig samme
// løype som motortreff (sidehenting, utdragsrangering, kildekontroll). Er
// artikkelen irrelevant, rangerer utdragsmotoren den bort av seg selv — derfor
// trengs ingen «er dette et oppslagsspørsmål?»-detektor.

// wikiUA: Wikimedia krever en identifiserbar klient. Uten dette svarer API-et
// 429 (bekreftet i test).
const wikiUA = "Nordavind/1.0 (https://app.nordawind.com; kontakt@nordawind.com)"

const wikiMaxResults = 3

// WikiSearch slår opp i Wikipedia — bokmål først, engelsk hvis bokmål er tomt
// (mange tekniske og internasjonale temaer finnes kun på engelsk). Fail-soft:
// enhver feil gir tom liste, aldri en feil som stopper søket.
func (c *Client) WikiSearch(ctx context.Context, query string) []Result {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	if r := c.wikiQuery(ctx, c.wikiNO, query); len(r) > 0 {
		return r
	}
	return c.wikiQuery(ctx, c.wikiEN, query)
}

// wikiQuery spør ett Wikipedia-API (base = full api.php-URL).
func (c *Client) wikiQuery(ctx context.Context, base, query string) []Result {
	if base == "" {
		return nil
	}
	u := base + "?action=query&list=search&format=json&srlimit=" +
		"5&srsearch=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", wikiUA)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var out struct {
		Query struct {
			Search []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}

	// Artikkel-URL utledes av tittelen; siden hentes siden av FetchPages som
	// et hvilket som helst annet treff.
	host := strings.TrimSuffix(base, "/w/api.php")
	var results []Result
	for _, s := range out.Query.Search {
		if strings.TrimSpace(s.Title) == "" {
			continue
		}
		results = append(results, Result{
			Title:       s.Title,
			URL:         host + "/wiki/" + url.PathEscape(strings.ReplaceAll(s.Title, " ", "_")),
			Description: StripHTML(s.Snippet), // snippet inneholder <span>-markering
		})
		if len(results) == wikiMaxResults {
			break
		}
	}
	return results
}
