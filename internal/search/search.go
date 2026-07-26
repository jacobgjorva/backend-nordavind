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

// Client søker via DuckDuckGos HTML-endepunkt — gratis, ingen API-nøkkel.
type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 8 * time.Second}}
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

func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
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
	fmt.Fprintf(&b, "Websøk (%s). Fakta om dette temaet skal KUN hentes fra kildene under — "+
		"aldri fra egen hukommelse. Oppgi URL når du siterer. Dekker ikke kildene svaret, "+
		"si at du ikke fant pålitelig informasjon:\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "\n### Kilde %d: %s — %s\n%s\n", i+1, r.Title, r.URL, r.Description)
		if i < len(pages) && pages[i] != "" {
			fmt.Fprintf(&b, "Sideinnhold:\n%s\n", pages[i])
		}
	}
	return b.String()
}
