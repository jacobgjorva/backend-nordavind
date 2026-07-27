package api

import "github.com/jacobgjorva/backend-nordavind/internal/search"

// mergeSources legger ekstra kilder (Wikipedia) etter motortreffene, uten
// duplikater på URL. Motortreff beholder rekkefølgen sin — de er rangert av
// søkemotoren; de ekstra er et supplement, og bærer turen alene når motorene
// er blokkert.
func mergeSources(primary, extra []search.Result) []search.Result {
	seen := make(map[string]bool, len(primary))
	out := make([]search.Result, 0, len(primary)+len(extra))
	for _, r := range primary {
		if r.URL == "" || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		out = append(out, r)
	}
	for _, r := range extra {
		if r.URL == "" || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		out = append(out, r)
	}
	return out
}
