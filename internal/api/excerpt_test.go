package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

// Ordretthets-invarianten: hver chunk skal være en ordrett delsekvens av
// input (linjer limt med \n) — grounding måler modellsvar mot denne teksten.
func TestSplitChunksVerbatim(t *testing.T) {
	page := "Styringsrenten er 4,25 prosent.\nNorges Bank besluttet dette 15. august 2026.\n" +
		strings.Repeat("En lengre setning om pengepolitikk som fyller plass i avsnittet. ", 20) +
		"\nOrg.nr. 937 884 117 er registrert."
	chunks := splitChunks(page)
	if len(chunks) == 0 {
		t.Fatal("ingen chunks")
	}
	for _, c := range chunks {
		for _, line := range strings.Split(c, "\n") {
			if !strings.Contains(page, line) {
				t.Fatalf("chunk-linje finnes ikke ordrett i input: %q", line)
			}
		}
		if len(c) > chunkMax+chunkMin {
			t.Fatalf("chunk over taket: %d tegn", len(c))
		}
	}
}

// Rangering med fake embedder: chunks som ligner spørringen skal vinne,
// rekkefølgen skal være stabil.
func TestRankExcerptsPicksRelevant(t *testing.T) {
	// Fake: vektor [1,0] for tekster med "rente", [0,1] ellers. Query = [1,0].
	embedFn := func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, tx := range texts {
			if strings.Contains(tx, "rente") {
				out[i] = []float32{1, 0}
			} else {
				out[i] = []float32{0, 1}
			}
		}
		return out, nil
	}
	pages := []string{
		"Om renten: styringsrenten er 4,25 prosent og renteutsiktene er stabile.\n" +
			strings.Repeat("Uvedkommende tekst om fotball og værmelding for Vestlandet. ", 10),
		strings.Repeat("Cookie-samtykke og personvernerklæring uten innhold. ", 10),
	}
	picked, err := rankExcerpts(context.Background(), embedFn, []float32{1, 0}, pages)
	if err != nil {
		t.Fatal(err)
	}
	if len(picked) == 0 {
		t.Fatal("ingenting plukket")
	}
	if !strings.Contains(picked[0].text, "rente") {
		t.Fatalf("toppavsnittet handler ikke om spørringen: %q", picked[0].text[:60])
	}
}

// Grounding-regresjonen: tall og navn i utdragene skal overleve slik at
// modellsvar som siterer dem IKKE flagges som dikting.
func TestExcerptContextGrounding(t *testing.T) {
	results := []search.Result{{Title: "Årsrapport", URL: "https://x.no/rapport", Description: "Nøkkeltall."}}
	picked := []sourceChunk{{source: 0, order: 0, score: 1,
		text: "Omsetningen ble 24,5 mill. kroner i 2025. Selskapet har org.nr. 937 884 117."}}
	ctxStr := formatExcerptContext("omsetning x", results, picked)

	answer := "Omsetningen var 24,5 mill. kroner, og org.nr. er 937 884 117."
	if off := groundingOffenders(answer, []string{ctxStr}); len(off) > 0 {
		t.Fatalf("kildekontrollen flagget korrekt sitert innhold: %v", off)
	}
}

func TestSearchCacheTTLAndReset(t *testing.T) {
	searchCacheMu.Lock()
	searchCache = map[string]searchCacheEntry{}
	searchCacheMu.Unlock()

	searchCachePut("Styringsrenten  NÅ?", "kontekst", []sourceRef{{Title: "t", URL: "u"}})
	if _, _, ok := searchCacheGet("styringsrenten nå?"); !ok {
		t.Fatal("normalisert nøkkel traff ikke")
	}
	// Utløpt oppføring skal ikke treffes.
	searchCacheMu.Lock()
	e := searchCache[searchCacheKey("styringsrenten nå?")]
	e.at = time.Now().Add(-searchCacheTTL - time.Minute)
	searchCache[searchCacheKey("styringsrenten nå?")] = e
	searchCacheMu.Unlock()
	if _, _, ok := searchCacheGet("styringsrenten nå?"); ok {
		t.Fatal("utløpt cache-oppføring ble servert")
	}
	// Tomt innhold caches aldri.
	searchCachePut("tom", "   ", nil)
	if _, _, ok := searchCacheGet("tom"); ok {
		t.Fatal("tomt resultat ble cachet")
	}
}

// Ulesbare kilder (403 fra Cloudflare o.l.) må MERKES — ellers leser modellen
// en overskrift som en besvart kilde og dikter resten («tekoligark»-saken).
func TestUnreadableSourceIsMarked(t *testing.T) {
	results := []search.Result{
		{Title: "Lest kilde", URL: "https://a.no", Description: "beskrivelse a"},
		{Title: "Årets ord", URL: "https://sprakradet.no/arets-ord", Description: "Språkrådet kårer årets ord."},
	}
	picked := []sourceChunk{{source: 0, order: 0, score: 1, text: "Innhold fra kilde som lot seg lese."}}
	ctxStr := formatExcerptContext("årets ord", results, picked)

	if !strings.Contains(ctxStr, "kunne ikke leses") {
		t.Fatal("kilde uten utdrag ble ikke merket som ulesbar")
	}
	// Den leste kilden skal ikke merkes.
	before := strings.Index(ctxStr, "Lest kilde")
	after := strings.Index(ctxStr, "Årets ord")
	mark := strings.Index(ctxStr, "kunne ikke leses")
	if mark < after || after < before {
		t.Fatal("merkingen havnet på feil kilde")
	}
}
