package search

import (
	"bytes"
	"context"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/ledongthuc/pdf"
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
	req.Header.Set("Accept", "text/html,application/pdf")

	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "application/pdf") || strings.HasSuffix(strings.ToLower(url), ".pdf") {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return ""
		}
		return extractPDF(raw, maxChars)
	}
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
	text = stripBoilerplate(text)

	if r := []rune(text); len(r) > maxChars {
		text = string(r[:maxChars])
	}
	return text
}

// extractPDF henter ren tekst fra en PDF — norske regnskaps- og offentlige
// data ligger ofte kun i PDF.
func extractPDF(raw []byte, maxChars int) (out string) {
	// pdf-biblioteket kan panikke på korrupte filer.
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return ""
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage() && b.Len() < maxChars*2; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		txt, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		b.WriteString(txt)
		b.WriteString("\n")
	}
	text := reSpace.ReplaceAllString(b.String(), " ")
	text = reLines.ReplaceAllString(strings.TrimSpace(text), "\n")
	if r2 := []rune(text); len(r2) > maxChars {
		text = string(r2[:maxChars])
	}
	return text
}

// stripBoilerplate fjerner meny/nav-støy: korte linjer uten tall (typisk
// lenkelister) droppes, slik at tegnbudsjettet brukes på innhold og tabeller.
func stripBoilerplate(text string) string {
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(strings.Fields(trimmed)) < 4 && !strings.ContainsAny(trimmed, "0123456789") {
			continue
		}
		keep = append(keep, trimmed)
	}
	if len(keep) == 0 {
		return text
	}
	return strings.Join(keep, "\n")
}
