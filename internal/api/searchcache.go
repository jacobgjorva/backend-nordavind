package api

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// Søke-cache: samme spørsmål innen TTL koster null oppstrøms-trafikk (søk,
// sidehenting OG embeddings) og gir byte-identisk grounding-basis. Dette er
// det viktigste vernet mot rate-limiting ved skala — hvert treff er et søk
// motorene aldri ser. Mønster fra intentwire.go: mutex + map, flush over max.

const (
	searchCacheTTL = 15 * time.Minute
	searchCacheMax = 500 // ~2-3 MB ved fulle kontekster
)

type searchCacheEntry struct {
	context string
	refs    []sourceRef
	at      time.Time
}

var (
	searchCacheMu sync.Mutex
	searchCache   = map[string]searchCacheEntry{}
)

var wsRe = regexp.MustCompile(`\s+`)

// searchCacheKey normaliserer spørringen — «Styringsrenten  nå?» og
// «styringsrenten nå?» er samme søk.
func searchCacheKey(query string) string {
	return wsRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(query)), " ")
}

func searchCacheGet(query string) (string, []sourceRef, bool) {
	searchCacheMu.Lock()
	defer searchCacheMu.Unlock()
	e, ok := searchCache[searchCacheKey(query)]
	if !ok || time.Since(e.at) > searchCacheTTL {
		return "", nil, false
	}
	return e.context, e.refs, true
}

// searchCachePut lagrer et VELLYKKET søk. Tomme/feilede resultater caches
// aldri — et forbigående motorproblem skal ikke fryses i et kvarter.
func searchCachePut(query, context string, refs []sourceRef) {
	if strings.TrimSpace(context) == "" {
		return
	}
	searchCacheMu.Lock()
	defer searchCacheMu.Unlock()
	if len(searchCache) >= searchCacheMax {
		searchCache = map[string]searchCacheEntry{}
	}
	searchCache[searchCacheKey(query)] = searchCacheEntry{context: context, refs: refs, at: time.Now()}
}
