package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

const (
	// Biter på ~600 tokens (~2400 tegn) med litt overlapp gir presis henting
	// uten å sprenge kontekst. Ren koding — ingen tokens brukt på oppdeling.
	chunkMaxChars = 2400
	chunkOverlap  = 200
)

// chunkText deler dokumentteksten i biter på avsnittsgrenser, ~chunkMaxChars
// hver med litt overlapp så mening ikke kuttes midt av. Deterministisk.
func chunkText(text string) []string {
	paras := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	var chunks []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			chunks = append(chunks, s)
		}
		b.Reset()
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Overskrider biten grensen: lukk den og start ny med litt overlapp.
		if b.Len() > 0 && b.Len()+len(p) > chunkMaxChars {
			prev := b.String()
			flush()
			if len(prev) > chunkOverlap {
				b.WriteString(prev[len(prev)-chunkOverlap:])
				b.WriteString("\n\n")
			}
		}
		// Ett veldig langt avsnitt splittes hardt på tegngrensen.
		for len(p) > chunkMaxChars {
			b.WriteString(p[:chunkMaxChars])
			flush()
			p = p[chunkMaxChars:]
		}
		b.WriteString(p)
		b.WriteString("\n\n")
	}
	flush()
	return chunks
}

// propositionalizeSystem gjør en dokument-seksjon om til atomære, RAG-
// optimaliserte utsagn. Fidelity-guardrail: konkret info bevares ordrett;
// kun fyll fjernes. Betales én gang ved opplasting, spares på hver henting.
const propositionalizeSystem = "Du gjør om en del av et internt bedriftsdokument til RAG-optimaliserte, " +
	"atomære utsagn. Hvert utsagn skal være kort, SELVSTENDIG (forståelig uten kontekst) og fortettet: " +
	"fjern fyll, høflighetsfraser, gjentakelser og navigasjon. BEVAR ordrett hvert faktum, tall, terskel, " +
	"dato, navn og hvert steg i en prosedyre — aldri endre, avrund eller utelat konkret informasjon, og " +
	"behold rekkefølgen på steg. Svar KUN med JSON: {\"statements\":[\"...\", \"...\"]}."

// propositionalize deler dokumentet i seksjoner og gjør hver om til atomære
// utsagn via et billig kall. Returnerer feil hvis ingenting kom ut, så
// kalleren kan falle tilbake til naiv oppdeling.
func (s *Server) propositionalize(ctx context.Context, text string) ([]string, error) {
	var out []string
	for _, section := range chunkText(text) {
		raw, err := s.llmComplete(ctx, propositionalizeSystem, section, 1200)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Statements []string `json:"statements"`
		}
		if json.Unmarshal([]byte(stripFences(raw)), &parsed) != nil {
			continue
		}
		for _, st := range parsed.Statements {
			if st = strings.TrimSpace(st); st != "" {
				out = append(out, st)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("tom propositionalisering")
	}
	return out, nil
}

// docSummarySystem ber om en kort tittel + ett sammendrag av dokumentet, som
// JSON. Ett billig kall — dokumentteksten går aldri gjennom modellen ellers.
const docSummarySystem = "Du får starten av et internt bedriftsdokument. Svar KUN med JSON " +
	"(ingen prat): {\"title\":\"<kort, beskrivende tittel, maks 6 ord>\",\"summary\":" +
	"\"<én presis setning om hva dokumentet dekker>\"}."

// handleCreateDocument lagrer et opplastet dokument: deler i biter, embedder
// hver bit, henter tittel+sammendrag med ett billig kall, og lagrer som en
// accepted doknode med bitene under. Dokumentteksten sendes aldri til modellen.
func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Filename string `json:"filename"`
		Title    string `json:"title"`
		Text     string `json:"text"`
		ChatID   string `json:"chat_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "ugyldig request")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeErr(w, http.StatusBadRequest, "dokumentet er tomt")
		return
	}

	// Propositionalisér til atomære RAG-enheter; fall tilbake til naiv
	// oppdeling så lagring aldri stopper på en AI-feil.
	parts, err := s.propositionalize(r.Context(), text)
	if err != nil {
		s.log.Warn("propositionalisering feilet, bruker naiv oppdeling", "err", err)
		parts = chunkText(text)
	}
	if len(parts) == 0 {
		writeErr(w, http.StatusBadRequest, "fant ikke tekst å lagre")
		return
	}

	title, summary := s.documentTitleSummary(r.Context(), req.Title, req.Filename, text)

	// Hver lapp = én proposisjon, forankret med dokumentets sammendrag som
	// kontekst (contextual retrieval). Embeddingen dekker context + tekst så
	// lappen matcher selv når spørsmålet gjelder dokumentet som helhet.
	notes := make([]store.KnowledgeNote, 0, len(parts))
	for _, p := range parts {
		vec, err := s.embed(r.Context(), summary+" "+p)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "kunne ikke indeksere dokumentet")
			return
		}
		notes = append(notes, store.KnowledgeNote{Text: p, Context: summary, Embedding: vec})
	}

	docID, err := s.store.CreateDocumentNotes(user.TenantID, store.DocumentInput{
		Filename: req.Filename, RawText: text, Title: title, Summary: summary,
	}, notes)
	if err != nil {
		s.log.Error("kunne ikke lagre dokument", "err", err)
		writeErr(w, http.StatusInternalServerError, "kunne ikke lagre dokumentet")
		return
	}
	s.log.Info("dokument lagret", "tittel", title, "lapper", len(notes))
	writeJSON(w, map[string]any{"id": docID, "title": title, "summary": summary, "chunks": len(notes)})
}

// documentTitleSummary gir tittel + sammendrag. Bruker oppgitt tittel hvis
// satt, ellers ett billig LLM-kall på starten av teksten; faller tilbake til
// filnavnet så en indeksering aldri stopper på AI-feil.
func (s *Server) documentTitleSummary(ctx context.Context, given, filename, text string) (string, string) {
	head := text
	if len(head) > 2000 {
		head = head[:2000]
	}
	raw, err := s.llmComplete(ctx, docSummarySystem, head, 120)
	var parsed struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err == nil {
		json.Unmarshal([]byte(stripFences(raw)), &parsed)
	}
	title := strings.TrimSpace(given)
	if title == "" {
		title = strings.TrimSpace(parsed.Title)
	}
	if title == "" {
		title = filenameTitle(filename)
	}
	return title, strings.TrimSpace(parsed.Summary)
}

// filenameTitle lager en lesbar tittel fra et filnavn (uten sti/endelse).
func filenameTitle(filename string) string {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.NewReplacer("_", " ", "-", " ").Replace(base)
	if base = strings.TrimSpace(base); base != "" {
		return base
	}
	return "Dokument"
}
