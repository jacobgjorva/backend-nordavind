package search

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Client søker via self-hostet SearXNG (primær, se searxng.go) med
// DuckDuckGos HTML-endepunkt som fallback — gratis, ingen API-nøkkel.
type Client struct {
	http     *http.Client
	searxURL string // tom = hopp rett til DDG (lokal dev uten SearXNG)
	ddgURL   string // overstyres kun i test
	wikiNO   string // Wikipedia bokmål (overstyres kun i test)
	wikiEN   string // Wikipedia engelsk — fallback når bokmål er tomt
	// Downed kalles med motornavn SearXNG melder som døde/blokkerte —
	// blokkeringsovervåking uten at denne pakken kjenner loggeren.
	Downed func(engines []string)
}

// NewClient lager søkeklienten. searxURL fra SEARXNG_URL; tom streng gir
// ren DDG-oppførsel (fail-open, null friksjon for utviklere uten SearXNG).
func NewClient(searxURL string) *Client {
	return &Client{
		http:     &http.Client{Timeout: 8 * time.Second},
		searxURL: strings.TrimSuffix(searxURL, "/"),
		ddgURL:   "https://html.duckduckgo.com/html/",
		wikiNO:   "https://no.wikipedia.org/w/api.php",
		wikiEN:   "https://en.wikipedia.org/w/api.php",
	}
}

type Result struct {
	Title       string
	URL         string
	Description string
}

var (
	reResult  = regexp.MustCompile(`(?s)<a rel="nofollow" class="result__a" href="([^"]+)">(.*?)</a>`)
	reSnippet = regexp.MustCompile(`(?s)<a class="result__snippet"[^>]*>(.*?)</a>`)
	reTags    = regexp.MustCompile(`<[^>]+>`)
)

// Search: SearXNG når konfigurert, ellers (eller ved feil) DDG. Fail-open —
// et dødt SearXNG skal aldri ta med seg websøket i fallet.
func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	if c.searxURL != "" {
		results, down, err := c.searxSearch(ctx, query)
		if len(down) > 0 && c.Downed != nil {
			c.Downed(down)
		}
		if err == nil {
			return results, nil
		}
		// Fall gjennom til DDG med feilen kun som kontekst i loggen hos kalleren.
	}
	return c.searchDDG(ctx, query)
}

// searchDDG er den gamle DuckDuckGo-scrapingen — beholdt uendret som fallback.
func (c *Client) searchDDG(ctx context.Context, query string) ([]Result, error) {
	u := c.ddgURL + "?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo: HTTP %d", resp.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	links := reResult.FindAllStringSubmatch(string(page), 6)
	snippets := reSnippet.FindAllStringSubmatch(string(page), 6)

	var results []Result
	for i, m := range links {
		desc := ""
		if i < len(snippets) {
			desc = clean(snippets[i][1])
		}
		results = append(results, Result{
			Title:       clean(m[2]),
			URL:         resolveDDG(m[1]),
			Description: desc,
		})
	}
	return results, nil
}

// resolveDDG pakker ut ekte URL fra DDGs redirect-lenker (//duckduckgo.com/l/?uddg=...).
func resolveDDG(href string) string {
	if u, err := url.Parse(html.UnescapeString(href)); err == nil {
		if real := u.Query().Get("uddg"); real != "" {
			return real
		}
	}
	return href
}

// StripHTML fjerner tagger og entiteter — brukes også av e-postlesing.
func StripHTML(s string) string { return clean(s) }

func clean(s string) string {
	return strings.TrimSpace(html.UnescapeString(reTags.ReplaceAllString(s, "")))
}

// FormatContext gjør resultatene og sideinnholdet om til kontekst modellen
// kan sitere. pages[i] er hentet tekst for results[i] (kan være tom).
func FormatContext(query string, results []Result, pages []string) string {
	var b strings.Builder
	// Samme kildekontrakt som utdragsveien (api.sourceHeader): fakta fra
	// kildene, men slutninger over dem er lov — og ønsket.
	fmt.Fprintf(&b, "Websøk (%s). Alle FAKTA om dette temaet — tall, navn, datoer, "+
		"hendelser — skal hentes fra kildene under, aldri fra egen hukommelse. "+
		"Du skal derimot TENKE med det du finner: trekk de slutningene fakta i "+
		"kildene gir grunnlag for, og si hva de betyr for det brukeren spurte om. "+
		"Mangler et faktum i kildene, si det — men ikke hold tilbake en slutning "+
		"som følger av det som faktisk står der. Oppgi URL når du siterer:\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "\n### Kilde %d: %s — %s\n%s\n", i+1, r.Title, r.URL, r.Description)
		if i < len(pages) && pages[i] != "" {
			fmt.Fprintf(&b, "Sideinnhold:\n%s\n", pages[i])
		}
	}
	return b.String()
}
