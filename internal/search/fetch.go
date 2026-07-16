package search

import (
	"context"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

var (
	reScript = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<noscript[^>]*>.*?</noscript>|<svg[^>]*>.*?</svg>|<head[^>]*>.*?</head>`)
	reBlock  = regexp.MustCompile(`(?i)</(p|div|li|tr|h[1-6])>|<br[^>]*>`)
	reSpace  = regexp.MustCompile(`[ \t\r\f]+`)
	reLines  = regexp.MustCompile(`\n{2,}`)
)

// FetchPages henter innholdet fra sidene parallelt og returnerer ren tekst
// per resultat (tom streng ved feil). Maks maxChars tegn per side.
func (c *Client) FetchPages(ctx context.Context, results []Result, maxChars int) []string {
	texts := make([]string, len(results))
	var wg sync.WaitGroup
	for i, r := range results {
		wg.Add(1)
		go func(i int, url string) {
			defer wg.Done()
			texts[i] = c.fetchPage(ctx, url, maxChars)
		}(i, r.URL)
	}
	wg.Wait()
	return texts
}

func (c *Client) fetchPage(ctx context.Context, url string, maxChars int) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")
	req.Header.Set("Accept", "text/html")

	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "text/html") && !strings.Contains(ct, "text/plain") {
		return ""
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}

	text := string(raw)
	text = reScript.ReplaceAllString(text, " ")
	text = reBlock.ReplaceAllString(text, "\n")
	text = reTags.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = reSpace.ReplaceAllString(text, " ")
	text = reLines.ReplaceAllString(strings.TrimSpace(text), "\n")

	if r := []rune(text); len(r) > maxChars {
		text = string(r[:maxChars])
	}
	return text
}
