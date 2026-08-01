package knowledge

import (
	"context"
	"strings"
)

// Package knowledge er kunnskapsgraf v2 (KUNNSKAP-V2-BLUEPRINT): henter
// intern kontekst for en tur — eller tier. Pakken eier UTVALGET; api-laget
// eier adaptere (embedding, store) og injeksjonen. Motoren kjenner ikke
// pakken og mottar kun ferdig tekst.
//
// Designregler (samme som motoren):
//   - Ingen nettverkskode her: Embedder og Source er kontrakter.
//   - TOM blokk er et gyldig og VANLIG svar — «alltid noe» var v1-feilen
//     som ga 2 700 tegn intern kunnskap på «takk for hjelpen».
//   - Terskler settes fra måling (kjøring 1 i KUNNSKAP-V2-MAALINGER.md),
//     endres kun med grønn eval.

// Embedder gjør tekst om til vektor. Implementeres av api-laget.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Candidate er én mulig kunnskapsbit, allerede scope-filtrert av kilden.
type Candidate struct {
	ID       string
	Title    string
	Text     string
	Extra    string  // kildekontekst (dokumenttittel o.l.)
	Sim      float64 // vektorlikhet mot spørsmålet; 0 for rene FTS-treff
	Source   string  // fact | document
	SourceID string  // dokument-id for dokument-biter (kantfrø)
}

// Source leverer scope-filtrerte kandidater. Implementeres av api-laget
// over store (scopeClause er DEN ENE synlighetsklausulen — se notescope.go).
type Source interface {
	Vector(ctx context.Context, vec []float32, k int) ([]Candidate, error)
	Keyword(ctx context.Context, tokens []string, k int) ([]Candidate, error)
	// Neighbors er kantutvidelsen: kunnskap KOBLET til de valgte bitene
	// (uttrukket node ↔ dokumentet den kom fra). Frøene er kandidat-id-er
	// og dokument-id-er; kilden håndhever scope som ellers.
	Neighbors(ctx context.Context, seeds []string, k int) ([]Candidate, error)
}

// Request er turens spørsmål i kunnskapens øyne.
type Request struct {
	Question string
}

// Block er svaret: teksten som injiseres (tom = injiser ingenting) og
// metadata for logg og eval.
type Block struct {
	Text  string
	Lines int
	Used  []string // kandidat-id-er som ble med (RecordNoteHits m.m.)
	Diag  Diag     // hvorfor det ble som det ble — alltid logget
}

// Diag er portens regnskap. Uten dette er «tom blokk» uskillelig fra
// «feilet stille», og det var nøyaktig blindsonen som gjorde at v2 leverte
// null i prod uten et eneste spor i loggen (2026-08-02).
type Diag struct {
	VecN, KwN, NbN int
	Top, Median    float64
	Reason         string // kort ord om utfallet
	Err            string
}

// Budsjettet: v2 er selektiv — relevans over volum. v1 sendte inntil 4 000
// tegn / 25 lapper; kalibreringen viste at det meste var støy.
const (
	candDepth   = 30
	maxLines    = 8
	charBudget  = 1600
	neighborCap = 3
	minQuery    = 12 // runer; kortere spørringer har ikke semantisk innhold
)

// Context er sømmen: hent det som er RELEVANT for spørsmålet, ellers intet.
func Context(ctx context.Context, emb Embedder, src Source, req Request) (Block, error) {
	q := strings.TrimSpace(req.Question)
	if len([]rune(q)) < minQuery {
		return Block{}, nil
	}
	vec, err := emb.Embed(ctx, q)
	if err != nil || len(vec) == 0 {
		d := Diag{Reason: "embed-feil", Err: errText(err)}
		// Fail-open til nøkkelord: embedding nede skal gi dårligere utvalg,
		// aldri tom kontekst PÅ GRUNN AV INFRASTRUKTUR (asymmetrien i v1).
		kws, kerr := src.Keyword(ctx, tokens(q), candDepth)
		d.KwN = len(kws)
		if kerr != nil {
			d.Err = errText(kerr)
		}
		if kerr != nil || len(kws) == 0 {
			return Block{Diag: d}, nil
		}
		b := render(gateKeywordOnly(kws, q))
		b.Diag = d
		return b, nil
	}
	var d Diag
	vecCands, verr := src.Vector(ctx, vec, candDepth)
	kwCands, kerr := src.Keyword(ctx, tokens(q), candDepth)
	d.VecN, d.KwN = len(vecCands), len(kwCands)
	if verr != nil || kerr != nil {
		d.Err = errText(verr) + errText(kerr)
	}
	if len(vecCands) > 0 {
		d.Top = vecCands[0].Sim
		sims := make([]float64, 0, len(vecCands))
		for _, c := range vecCands {
			sims = append(sims, c.Sim)
		}
		d.Median = median(sims)
	}
	picked := gate(vecCands, kwCands)
	if len(picked) == 0 {
		d.Reason = "porten avviste alt"
	} else {
		d.Reason = "treff"
	}
	// Kantutvidelse KUN når porten fant noe: naboene til et relevant treff
	// er relevante i kraft av koblingen (fryseromskoden hører til
	// varemottaket), men et tomt utvalg skal aldri fylles av naboer.
	if len(picked) > 0 {
		seeds := make([]string, 0, len(picked)*2)
		have := map[string]bool{}
		for _, c := range picked {
			seeds = append(seeds, c.ID)
			have[c.ID] = true
			if c.SourceID != "" {
				seeds = append(seeds, c.SourceID)
			}
		}
		if nbs, err := src.Neighbors(ctx, seeds, neighborCap); err == nil {
			d.NbN = len(nbs)
			for _, n := range nbs {
				if !have[n.ID] {
					have[n.ID] = true
					picked = append(picked, n)
				}
			}
		} else {
			d.Err += errText(err)
		}
	}
	b := render(picked)
	b.Diag = d
	return b, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error() + " "
}

// tokens er innholdsordene i spørringen (FTS-benet).
func tokens(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9' ||
			r == 'æ' || r == 'ø' || r == 'å')
	}) {
		if len([]rune(w)) >= 4 && !stopword[w] {
			out = append(out, w)
		}
	}
	return out
}

var stopword = map[string]bool{
	"hvordan": true, "hvilke": true, "hvilken": true, "hvorfor": true,
	"denne": true, "dette": true, "disse": true, "skal": true, "kan": true,
	"med": true, "for": true, "til": true, "har": true, "hva": true,
	"gjelder": true, "våre": true, "vårt": true,
}

func render(picked []Candidate) Block {
	if len(picked) == 0 {
		return Block{}
	}
	var b strings.Builder
	b.WriteString("Relevant intern kunnskap (bedriftens egen, del kun med brukere i samme virksomhet):\n")
	used := make([]string, 0, len(picked))
	lines := 0
	for _, c := range picked {
		line := "- " + c.Text
		if c.Extra != "" {
			line += " (fra: " + c.Extra + ")"
		}
		if b.Len()+len(line) > charBudget || lines >= maxLines {
			break
		}
		b.WriteString(line + "\n")
		used = append(used, c.ID)
		lines++
	}
	if lines == 0 {
		return Block{}
	}
	return Block{Text: b.String(), Lines: lines, Used: used}
}
