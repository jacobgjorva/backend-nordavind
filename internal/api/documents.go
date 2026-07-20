package api

import (
	"context"
	"encoding/json"
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

	parts := chunkText(text)
	if len(parts) == 0 {
		writeErr(w, http.StatusBadRequest, "fant ikke tekst å lagre")
		return
	}

	title, summary := s.documentTitleSummary(r.Context(), req.Title, req.Filename, text)

	// Embed sammendraget (til node-ranking) og hver bit (til bit-henting).
	summaryVec, err := s.embed(r.Context(), title+". "+summary)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "kunne ikke indeksere dokumentet")
		return
	}
	chunks := make([]store.DocumentChunk, 0, len(parts))
	for _, p := range parts {
		vec, err := s.embed(r.Context(), p)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "kunne ikke indeksere dokumentet")
			return
		}
		chunks = append(chunks, store.DocumentChunk{Content: p, Embedding: vec})
	}

	node, err := s.store.CreateDocument(user.TenantID, store.KnowledgeNode{
		Type: "dokument", Status: "accepted", Title: title, Summary: summary,
		ChatID: req.ChatID, UserID: user.ID, Embedding: summaryVec,
	}, chunks)
	if err != nil {
		s.log.Error("kunne ikke lagre dokument", "err", err)
		writeErr(w, http.StatusInternalServerError, "kunne ikke lagre dokumentet")
		return
	}
	s.log.Info("dokument lagret", "tittel", title, "biter", len(chunks))
	writeJSON(w, map[string]any{"id": node.ID, "title": title, "summary": summary, "chunks": len(chunks)})
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
