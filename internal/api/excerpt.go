package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

// Utdragsmotoren: i stedet for å sende modellen rå sidetekst (9-10k tegn per
// søk), velges de avsnittene som faktisk handler om spørsmålet — rangert med
// embeddings, ~4-5k tegn med HØYERE relevans.
//
// UFRAVIKELIG (grounding.go): utdrag = å VELGE ordrette avsnitt. Aldri
// omskriv, aldri normaliser, aldri klipp midt i — kildekontrollen måler
// modellens svar mot denne teksten byte for byte.

const (
	chunkMin     = 200 // linjer slås sammen til minst dette
	chunkMax     = 700 // og aldri mer enn dette (ren konkatenering)
	maxChunks    = 60  // tak på embedding-kallet (ett batch-kall)
	topExcerpts  = 8   // avsnitt totalt på tvers av kildene
	excerptPages = 4   // sider som hentes (opp fra 3 — rangeringen har råd)
	excerptChars = 6000
)

// splitChunks deler sidetekst i avsnitt på chunkMin-chunkMax tegn. fetchPage
// gir én linje per tekstblokk; korte nabolinjer slås sammen med \n slik at
// hver chunk forblir en ordrett delsekvens av input.
func splitChunks(page string) []string {
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(page, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		// Én lang linje (fetchPage limer blokker) deles på ordgrenser —
		// hver bit er fortsatt en ordrett delstreng av originalen.
		for len(line) > chunkMax {
			cut := strings.LastIndex(line[:chunkMax], " ")
			if cut < chunkMin {
				cut = chunkMax
			}
			piece := strings.TrimSpace(line[:cut])
			line = strings.TrimSpace(line[cut:])
			if cur.Len() > 0 {
				flush()
			}
			cur.WriteString(piece)
			flush()
		}
		if cur.Len() > 0 && cur.Len()+len(line) > chunkMax {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
		if cur.Len() >= chunkMin {
			flush()
		}
	}
	flush()
	return chunks
}

// sourceChunk husker hvilken kilde et avsnitt kom fra og plassen i dokumentet.
type sourceChunk struct {
	source int // indeks i results
	order  int // dokumentrekkefølge innen kilden
	text   string
	score  float64
}

// rankExcerpts velger de topExcerpts mest relevante avsnittene på tvers av
// kildene. embedFn injiseres for testbarhet (prod: s.embedBatch).
func rankExcerpts(ctx context.Context, embedFn func(context.Context, []string) ([][]float32, error),
	queryVec []float32, pages []string) ([]sourceChunk, error) {

	// Round-robin-klipp: ingen enkeltside får fylle hele budsjettet.
	perPage := make([][]string, len(pages))
	total := 0
	for i, p := range pages {
		perPage[i] = splitChunks(p)
		total += len(perPage[i])
	}
	for total > maxChunks {
		// Klipp fra slutten av den siden som har flest — bunnen av en side er
		// oftest boilerplate uansett.
		big := 0
		for i := range perPage {
			if len(perPage[i]) > len(perPage[big]) {
				big = i
			}
		}
		perPage[big] = perPage[big][:len(perPage[big])-1]
		total--
	}

	var all []sourceChunk
	var texts []string
	for src, chunks := range perPage {
		for ord, c := range chunks {
			all = append(all, sourceChunk{source: src, order: ord, text: c})
			texts = append(texts, c)
		}
	}
	if len(all) == 0 {
		return nil, nil
	}

	vecs, err := embedFn(ctx, texts)
	if err != nil {
		return nil, err
	}
	for i := range all {
		all[i].score = cosine(queryVec, vecs[i])
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > topExcerpts {
		all = all[:topExcerpts]
	}
	return all, nil
}

// formatExcerptContext bygger modellkonteksten: samme kontrakt som
// search.FormatContext (grounding-headeren), men med rangerte utdrag i
// dokumentrekkefølge per kilde, beste kilde først (agentplan klipper på 4000).
func formatExcerptContext(query string, results []search.Result, picked []sourceChunk) string {
	bySource := map[int][]sourceChunk{}
	var totalScore = map[int]float64{}
	for _, c := range picked {
		bySource[c.source] = append(bySource[c.source], c)
		totalScore[c.source] += c.score
	}
	order := make([]int, 0, len(bySource))
	for src := range bySource {
		order = append(order, src)
	}
	sort.Slice(order, func(i, j int) bool { return totalScore[order[i]] > totalScore[order[j]] })

	var b strings.Builder
	fmt.Fprintf(&b, "Websøk (%s). Fakta om dette temaet skal KUN hentes fra kildene under — "+
		"aldri fra egen hukommelse. Oppgi URL når du siterer. Dekker ikke kildene svaret, "+
		"si at du ikke fant pålitelig informasjon:\n", query)
	n := 0
	for _, src := range order {
		r := results[src]
		n++
		fmt.Fprintf(&b, "\n### Kilde %d: %s — %s\n%s\n", n, r.Title, r.URL, r.Description)
		chunks := bySource[src]
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].order < chunks[j].order })
		b.WriteString("Utdrag:\n")
		for _, c := range chunks {
			b.WriteString(c.text)
			b.WriteString("\n---\n")
		}
	}
	// Kilder uten utdrag tas med som tittel+beskrivelse — men MERKET. Uten
	// merkingen leste modellen en overskrift som en besvart kilde og diktet
	// resten (Cloudflare-blokkerte sider gir 403 og dermed tom tekst).
	for src, r := range results {
		if _, ok := bySource[src]; ok {
			continue
		}
		n++
		fmt.Fprintf(&b, "\n### Kilde %d: %s — %s\n%s\n", n, r.Title, r.URL, r.Description)
		b.WriteString("MERK: selve siden kunne ikke leses. Du vet KUN det som står i " +
			"beskrivelsen over — gjett aldri på innholdet, og si heller at kilden ikke lot seg lese.\n")
	}
	return b.String()
}
