package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"sync"
	"sync/atomic"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

const (
	embeddingModel = "qwen3-embedding-8b"
	// qwen3-embedding scorer urelatert tekst ~0.21 og klart relevant ~0.31–0.53.
	// 0.28 er vektor-gulvet som skiller signal fra støy.
	vecFloor   = 0.28
	candDepth  = 30   // hvor mange kandidater fra hver kilde (vektor / nøkkelord)
	rrfK       = 60.0 // RRF-konstant (standard); demper topprangeringens dominans
	noteMaxN   = 25   // maks antall lapper (budsjettet er den reelle grensen)
	noteBudget = 4000 // hardt tak på injisert tekst (tegn)
)

// knowledgeFor henter kunnskapskonteksten for siste brukermelding i en
// forespørsel, hvis brukeren er innlogget og tenanten har noder.
func (s *Server) knowledgeFor(ctx context.Context, full map[string]any) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok || user.TenantID == "" {
		return ""
	}
	return s.knowledgeContext(ctx, user.TenantID, lastUserText(full))
}

// lastUserText henter ren tekst fra siste brukermelding (håndterer både
// streng-innhold og multimodale deler).
func lastUserText(full map[string]any) string {
	msgs, _ := full["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			var b strings.Builder
			for _, part := range c {
				if pm, ok := part.(map[string]any); ok && pm["type"] == "text" {
					if t, ok := pm["text"].(string); ok {
						b.WriteString(t)
						b.WriteString(" ")
					}
				}
			}
			return b.String()
		}
		return ""
	}
	return ""
}

// embed lager en vektor for en tekst via Scaleways embeddings-endepunkt.
// Global cache for query-embeddings. Embeddings er tenant-uavhengige (samme
// tekst → samme vektor), så identiske spørsmål slipper et nytt kall.
var (
	embedCache  sync.Map // string -> []float32
	embedCacheN atomic.Int64
)

const embedCacheMax = 1000

// embedCached returnerer en cachet embedding for teksten, ellers henter og
// cacher den (bundet størrelse).
func (s *Server) embedCached(ctx context.Context, text string) ([]float32, error) {
	key := strings.ToLower(strings.TrimSpace(text))
	if v, ok := embedCache.Load(key); ok {
		return v.([]float32), nil
	}
	vec, err := s.embed(ctx, text)
	if err != nil {
		return nil, err
	}
	if embedCacheN.Load() < embedCacheMax {
		if _, loaded := embedCache.LoadOrStore(key, vec); !loaded {
			embedCacheN.Add(1)
		}
	}
	return vec, nil
}

// hedgeAfter er hvor lenge vi venter på det første embedding-kallet før vi
// sender ett til i parallell. Leverandøren har lang, TILFELDIG hale (målt
// 0,5-21 s fra prod på identiske kall), og halen rammer sjelden to kall
// samtidig. Vi bruker det første svaret som kommer.
const hedgeAfter = 400 * time.Millisecond

func (s *Server) embed(ctx context.Context, text string) ([]float32, error) {
	type res struct {
		vec []float32
		err error
	}
	out := make(chan res, 2)
	call := func() {
		vecs, err := s.embedBatch(ctx, []string{text})
		if err != nil {
			out <- res{nil, err}
			return
		}
		out <- res{vecs[0], nil}
	}
	go call()

	timer := time.NewTimer(hedgeAfter)
	defer timer.Stop()
	var firstErr error
	for pending := 1; pending > 0; {
		select {
		case <-timer.C:
			// Første kall drøyer: send ett til og ta det som svarer først.
			go call()
			pending++
			timer.Stop()
		case r := <-out:
			pending--
			if r.err == nil {
				return r.vec, nil
			}
			if firstErr == nil {
				firstErr = r.err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, firstErr
}

// embedBatch embedder mange tekster i ETT kall (utdragslaget rangerer opptil
// 60 avsnitt per søk — 60 HTTP-kall ville vært både tregt og dumt). Svar
// sorteres på index-feltet; API-et garanterer ikke rekkefølge.
func (s *Server) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	url := strings.TrimSuffix(s.cfg.UpstreamBaseURL, "/") + "/embeddings"
	body, _ := json.Marshal(map[string]any{"model": embeddingModel, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.UpstreamAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.UpstreamAPIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	s.countLLM(ctx, embeddingModel, "embedding", out.Usage.PromptTokens, 0)
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: fikk %d vektorer for %d tekster", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("embeddings: ugyldig index %d", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

var ftsTokenRe = regexp.MustCompile(`\p{L}[\p{L}\p{N}]+`)

// ftsStopwords: norske funksjonsord som bærer null søke-hensikt. Uten dette
// filteret OR-matcher «kan du … om … til meg» praktisk talt alle lapper, og
// irrelevante meldinger får injisert kunnskap (målt i knowledge-eval:
// 392-995 tegn støy). Innholdsord slipper alltid gjennom.
var ftsStopwords = map[string]bool{
	"og": true, "i": true, "på": true, "til": true, "for": true, "med": true,
	"av": true, "om": true, "er": true, "var": true, "det": true, "den": true,
	"de": true, "en": true, "et": true, "ei": true, "jeg": true, "du": true,
	"vi": true, "han": true, "hun": true, "dere": true, "meg": true, "deg": true,
	"oss": true, "min": true, "din": true, "vår": true, "hva": true, "hvem": true,
	"hvor": true, "når": true, "hvordan": true, "hvorfor": true, "hvilke": true,
	"hvilken": true, "kan": true, "skal": true, "vil": true, "må": true,
	"har": true, "hadde": true, "blir": true, "ble": true, "som": true,
	"at": true, "ikke": true, "noe": true, "noen": true, "denne": true,
	"dette": true, "der": true, "her": true, "da": true, "så": true, "seg": true,
	"sin": true, "sitt": true, "våre": true, "mine": true, "eller": true,
	"men": true, "også": true, "bare": true, "litt": true, "lite": true,
	"veldig": true, "helt": true, "egentlig": true, "gjerne": true, "takk": true,
}

// contentTokens plukker innholdsordene fra fri tekst (stoppord filtreres) —
// grunnlaget for nøkkelordsøket i begge dialekter og enslig-ord-vernet.
func contentTokens(query string) []string {
	toks := ftsTokenRe.FindAllString(strings.ToLower(query), -1)
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if !ftsStopwords[t] {
			out = append(out, t)
		}
	}
	return out
}

// knowledgeContext henter den mest relevante interne kunnskapen for spørsmålet
// fra den felles lappe-skuffen, som en hybrid av vektor-likhet og nøkkelord
// (BM25), fusjonert med Reciprocal Rank Fusion. Returnerer tom streng når
// ingenting er relevant. Rangeringsbasert fusjon → ingen skjør absoluttterskel.
func (s *Server) knowledgeContext(ctx context.Context, tenantID, query string) string {
	// Svært korte meldinger («ok», «takk») bærer ingen søkbar hensikt.
	if len([]rune(strings.TrimSpace(query))) < 12 {
		return ""
	}
	// Embeddingen er et nettverkskall til en leverandør med lang hale (målt
	// 0,5-21 s fra prod). Feiler den, hentes kunnskapen på nøkkelord alene i
	// stedet for å droppe den: BM25-indeksen ligger lokalt i databasen, og et
	// treff derfra er uendelig mye bedre enn «jeg har ikke tilgang».
	qvec, err := s.embedCached(ctx, query)
	if err != nil {
		s.log.Warn("embedding feilet — henter kunnskap på nøkkelord alene", "err", err)
		return s.keywordContext(tenantID, query)
	}

	// Vektor-kandidater: cosine over gulvet, rangert (best først).
	type sc struct {
		id    string
		score float64
	}
	var vec []sc
	byID := map[string]store.KnowledgeNote{}
	if s.store.IsPostgres() {
		// Søket skjer I databasen (pgvector) — kun topp-kandidatene krysser
		// grensa, uansett hvor stor kunnskapsbasen er.
		top, sims, err := s.store.TopNotesByEmbedding(tenantID, qvec, candDepth)
		if err != nil {
			s.log.Warn("vektorsøk feilet", "err", err)
			return ""
		}
		for i, n := range top {
			byID[n.ID] = n
			if sims[i] >= vecFloor {
				vec = append(vec, sc{n.ID, sims[i]})
			}
		}
	} else {
		// SQLite: last alle og regn i Go (fint på små baser).
		notes, _ := s.store.AcceptedNotes(tenantID)
		if len(notes) == 0 {
			return ""
		}
		for _, n := range notes {
			byID[n.ID] = n
			if c := cosine(qvec, n.Embedding); c >= vecFloor {
				vec = append(vec, sc{n.ID, c})
			}
		}
		sort.Slice(vec, func(i, j int) bool { return vec[i].score > vec[j].score })
		if len(vec) > candDepth {
			vec = vec[:candDepth]
		}
	}

	// Nøkkelord-kandidater, allerede rangert av databasen. Delstrengtreffene
	// legges bak: norsk skriver sammensatt, og «møte» finnes ikke som eget
	// ord i «Mandagsmøte» — uten dette faller nettopp de spørsmålene ut.
	qTokens := contentTokens(query)
	fts, _ := s.store.SearchNotesFTS(tenantID, qTokens, candDepth)
	if sub, err := s.store.SearchNotesSubstring(tenantID, qTokens, candDepth); err == nil {
		inFTS := map[string]bool{}
		for _, id := range fts {
			inFTS[id] = true
		}
		for _, id := range sub {
			if !inFTS[id] {
				fts = append(fts, id)
			}
		}
	}
	// Postgres: FTS-treff utenfor vektor-toppen må hentes inn i kartet.
	if s.store.IsPostgres() {
		var missing []string
		for _, id := range fts {
			if _, ok := byID[id]; !ok {
				missing = append(missing, id)
			}
		}
		if extra, err := s.store.NotesByIDs(tenantID, missing); err == nil {
			for _, n := range extra {
				byID[n.ID] = n
			}
		}
	}

	// Enslig-ord-vernet: en kandidat UTEN vektor-støtte må treffe minst to
	// distinkte innholdsord fra spørsmålet. Ett tilfeldig felles ord («våren»)
	// er støy, ikke intern kunnskap (målt i knowledge-eval).
	inVec := map[string]bool{}
	for _, v := range vec {
		inVec[v.id] = true
	}
	qToks := map[string]bool{}
	for _, t := range ftsTokenRe.FindAllString(strings.ToLower(query), -1) {
		if !ftsStopwords[t] {
			qToks[t] = true
		}
	}
	kept := fts[:0]
	for _, id := range fts {
		if inVec[id] {
			kept = append(kept, id)
			continue
		}
		note := byID[id]
		text := strings.ToLower(note.Text + " " + note.Context)
		matches := 0
		for t := range qToks {
			if strings.Contains(text, t) {
				matches++
			}
		}
		if matches >= 2 {
			kept = append(kept, id)
		}
	}
	fts = kept

	// RRF-fusjon: hver liste bidrar 1/(k + rangering).
	rrf := map[string]float64{}
	for rank, v := range vec {
		rrf[v.id] += 1.0 / (rrfK + float64(rank+1))
	}
	for rank, id := range fts {
		rrf[id] += 1.0 / (rrfK + float64(rank+1))
	}
	if len(rrf) == 0 {
		return ""
	}

	fused := make([]string, 0, len(rrf))
	for id := range rrf {
		fused = append(fused, id)
	}
	sort.Slice(fused, func(i, j int) bool {
		if rrf[fused[i]] != rrf[fused[j]] {
			return rrf[fused[i]] > rrf[fused[j]]
		}
		// Lik score: nyeste kunnskap vinner (ferskhet).
		return byID[fused[i]].CreatedAt.After(byID[fused[j]].CreatedAt)
	})

	// Kant-utvidelse (G2): naboene til topptreffene i kunnskapsgrafen tas med
	// bakerst, innenfor samme budsjett. En dokument-bit drar inn de destillerte
	// prosess/regel-nodene som er koblet til dokumentet sitt — og omvendt.
	neighbors := s.expandNeighbors(tenantID, fused, byID)

	// Bruks-telling (governance v2): registrer at lappene faktisk ble hentet.
	used := append(append([]string{}, fused...), neighbors...)
	go s.store.RecordNoteHits(used)

	var b strings.Builder
	b.WriteString("Relevant intern kunnskap (bruk der det passer; siter kilden når du bruker et dokument):\n")
	budget, n := noteBudget, 0
	write := func(id, prefix string) {
		note := byID[id]
		text := note.Text
		if note.Context != "" {
			text = note.Context + " " + text
		}
		src := "notat"
		if note.SourceType == "document" && note.Title != "" {
			src = "«" + note.Title + "»"
		}
		fmt.Fprintf(&b, "- %s(fra %s) %s\n", prefix, src, text)
		budget -= len(text)
		n++
	}
	for _, id := range fused {
		if n >= noteMaxN || budget <= 0 {
			break
		}
		write(id, "")
	}
	for _, id := range neighbors {
		if n >= noteMaxN || budget <= 0 {
			break
		}
		write(id, "(relatert) ")
	}
	return b.String()
}

// keywordContext er reserveveien når vektorsøket ikke kan kjøres: rent BM25
// mot den lokale indeksen. Samme format og budsjett som den vanlige veien, og
// samme enslig-ord-vern — ett tilfeldig felles ord er støy, ikke kunnskap.
func (s *Server) keywordContext(tenantID, query string) string {
	toks := contentTokens(query)
	ids, _ := s.store.SearchNotesFTS(tenantID, toks, candDepth)
	// Norsk skriver sammensatt: «møte» står inne i «Mandagsmøte» og fanges
	// ikke av ordbasert søk. Delstrengtreffene legges bak ordtreffene.
	if sub, err := s.store.SearchNotesSubstring(tenantID, toks, candDepth); err == nil {
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		for _, id := range sub {
			if !seen[id] {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return ""
	}
	notes, err := s.store.NotesByIDs(tenantID, ids)
	if err != nil || len(notes) == 0 {
		return ""
	}
	byID := map[string]store.KnowledgeNote{}
	for _, n := range notes {
		byID[n.ID] = n
	}
	qToks := map[string]bool{}
	for _, t := range ftsTokenRe.FindAllString(strings.ToLower(query), -1) {
		if !ftsStopwords[t] {
			qToks[t] = true
		}
	}
	var b strings.Builder
	b.WriteString("Relevant intern kunnskap (bruk der det passer; siter kilden når du bruker et dokument):\n")
	budget, n := noteBudget, 0
	var used []string
	for _, id := range ids {
		if n >= noteMaxN || budget <= 0 {
			break
		}
		note, ok := byID[id]
		if !ok {
			continue
		}
		text := strings.ToLower(note.Text + " " + note.Context)
		matches := 0
		for t := range qToks {
			if strings.Contains(text, t) {
				matches++
			}
		}
		if matches < 2 {
			continue
		}
		body := note.Text
		if note.Context != "" {
			body = note.Context + " " + body
		}
		src := "notat"
		if note.SourceType == "document" && note.Title != "" {
			src = "«" + note.Title + "»"
		}
		fmt.Fprintf(&b, "- (fra %s) %s\n", src, body)
		budget -= len(body)
		n++
		used = append(used, id)
	}
	if n == 0 {
		return ""
	}
	go s.store.RecordNoteHits(used)
	return b.String()
}

// expandSeedTop / expandMaxN: hvor mange topptreff som utvides, og maks antall
// naboer som tas inn. Små med vilje — naboer er tillegg, aldri hovedinnhold.
const (
	expandSeedTop = 5
	expandMaxN    = 5
)

// expandNeighbors finner 1-hopps grafnaboer til de øverste treffene: for
// fakta/uttrukne noder er nøkkelen node-id-en (== lapp-id), for dokument-biter
// dokument-noden (source_id). Returnerer lapp-ID-er som ikke alt er i treffene.
func (s *Server) expandNeighbors(tenantID string, fused []string, byID map[string]store.KnowledgeNote) []string {
	seen := map[string]bool{}
	var keys []string
	addKey := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for i, id := range fused {
		if i >= expandSeedTop {
			break
		}
		note := byID[id]
		if note.SourceType == "document" {
			addKey(note.SourceID)
		} else {
			addKey(id)
		}
	}
	edges, err := s.store.EdgesTouching(tenantID, keys)
	if err != nil || len(edges) == 0 {
		return nil
	}
	inFused := map[string]bool{}
	for _, id := range fused {
		inFused[id] = true
	}
	// Postgres holder bare kandidatene i minnet — hent nabo-lapper som mangler
	// (kun aksepterte lapper returneres; dok/pending-noder finnes ikke der).
	if s.store.IsPostgres() {
		var missing []string
		for _, e := range edges {
			for _, cand := range []string{e.FromID, e.ToID} {
				if !seen[cand] && !inFused[cand] {
					if _, ok := byID[cand]; !ok {
						missing = append(missing, cand)
					}
				}
			}
		}
		if extra, err := s.store.NotesByIDs(tenantID, missing); err == nil {
			for _, n := range extra {
				byID[n.ID] = n
			}
		}
	}
	var out []string
	for _, e := range edges {
		for _, cand := range []string{e.FromID, e.ToID} {
			if seen[cand] || inFused[cand] {
				continue
			}
			// Kun naboer som finnes som aksepterte lapper (byID) tas inn —
			// dok-noder og pending-noder har ingenting å injisere.
			if _, ok := byID[cand]; !ok {
				continue
			}
			seen[cand] = true
			out = append(out, cand)
			if len(out) >= expandMaxN {
				return out
			}
		}
	}
	return out
}
