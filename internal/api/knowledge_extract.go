package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// extractModel er en billig modell for bakgrunns-uttrekk av bransjekunnskap.
const extractModel = "mistral-small-3.2-24b-instruct-2506"

const extractSystem = "Trekk ut BEDRIFTS-INTERN operasjonell innsikt brukeren har lært AI-en i " +
	"samtalen: interne rutiner, egne fagtermer, hvem-gjør-hva, faste regler/terskler spesifikke for " +
	"bedriften. Lakmustest: ville en kompetent AI visst dette uten å få det fortalt? Hvis ja, utelat. " +
	"Ta ALDRI med allmennkunnskap, definisjoner, det AI-en selv svarte, nettsøk-resultater, " +
	"forretningsstrategi, konkrete tall (omsetning/priser/lønn), personopplysninger, navn, engangsting, " +
	"meninger eller sensitivt. Behold terskler som er del av en fast rutine («ordrer over X må " +
	"godkjennes»). Ved tvil: utelat. De fleste samtaler gir INGEN noder. Ingen dubletter av eksisterende " +
	"(listen under). Maks 3. Svar KUN med JSON: {\"nodes\":[{\"type\":\"term|prosess|regel|entitet\"," +
	"\"title\":\"kort navn\",\"summary\":\"én presis setning\",\"relations\":[{\"to\":\"tittel\"," +
	"\"relation\":\"er del av|kommer før|betyr|brukes i|gjelder for\"}]}]}"

type extractedNode struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Relations []struct {
		To       string `json:"to"`
		Relation string `json:"relation"`
	} `json:"relations"`
}

// handleExtractKnowledge kjører uttrekk etter en utveksling og returnerer
// FORSLAGENE til klienten (governance v2): kunnskap lagres først når KILDEN
// bekrefter med ett klikk i chatten — aldri en admin-kø.
func (s *Server) handleExtractKnowledge(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		ChatID   string `json:"chat_id"`
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	if !worthExtracting(req.Question) {
		// Ikke bruk et LLM-kall på småprat/spørsmål uten bedriftsintern forklaring.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Hjernen fylles i BAKGRUNNEN av samme utveksling: påstander og
	// prosedyrer krever ingen bekreftelse for å være nyttige, og de kan
	// rettes i grafen. Kjører den ikke, er alt som før.
	if s.cfg.BrainMode == "on" {
		go func(tenant, uid, chat, q, a string) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, userKey, store.User{ID: uid, TenantID: tenant})
			s.ExtractToBrain(ctx, tenant, uid, q+"\n\n(AI svarte: "+a+")", "chat", chat)
		}(user.TenantID, user.ID, req.ChatID, req.Question, req.Answer)
	}
	proposals := s.extractProposals(r.Context(), user.TenantID, req.Question, req.Answer)
	writeJSON(w, map[string]any{"proposals": proposals})
}

// handleConfirmKnowledge lagrer et bekreftet forslag: accepted node + lapp i
// retrieval-skuffen, med automatisk dublettvakt (nyeste erstatter).
func (s *Server) handleConfirmKnowledge(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Type    string `json:"type"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
		ChatID  string `json:"chat_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Summary = strings.TrimSpace(req.Summary)
	if req.Title == "" || req.Summary == "" {
		http.Error(w, "mangler tittel eller innhold", http.StatusBadRequest)
		return
	}
	vec, err := s.embed(r.Context(), req.Title+". "+req.Summary)
	if err != nil {
		http.Error(w, "kunne ikke indeksere", http.StatusBadGateway)
		return
	}
	if err := s.ingestFact(user.TenantID, user.ID, req.ChatID, req.Type, req.Title, req.Summary, vec); err != nil {
		http.Error(w, "kunne ikke lagre", http.StatusInternalServerError)
		return
	}
	s.log.Info("kunnskap bekreftet av kilden", "tittel", req.Title)
	w.WriteHeader(http.StatusNoContent)
}

// handleRememberMessage lagrer en chatmelding brukeren eksplisitt vil huske
// (minnekort-ikonet): destiller til noder der det går, ellers lagres selve
// meldingen som én lapp — et eksplisitt klikk skal ALLTID gi et minne.
func (s *Server) handleRememberMessage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Text   string `json:"text"`
		ChatID string `json:"chat_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(req.Text)
	proposals := s.extractProposals(r.Context(), user.TenantID, text, "")
	if len(proposals) == 0 {
		title := text
		if r := []rune(title); len(r) > 60 {
			title = string(r[:60]) + "…"
		}
		proposals = []extractedNode{{Type: "term", Title: title, Summary: text}}
	}
	saved := 0
	for _, p := range proposals {
		vec, err := s.embed(r.Context(), p.Title+". "+p.Summary)
		if err != nil {
			continue
		}
		if err := s.ingestFact(user.TenantID, user.ID, req.ChatID, p.Type, p.Title, p.Summary, vec); err != nil {
			continue
		}
		saved++
	}
	if saved == 0 {
		http.Error(w, "kunne ikke lagre", http.StatusBadGateway)
		return
	}
	s.log.Info("melding lagret til minnet", "noder", saved)
	writeJSON(w, map[string]any{"saved": saved})
}

// autoDupThreshold: målt mot ekte aksepterte fakta — distinkte par ligger
// under 0.58; over 0.75 er det samme faktum i ny form → nyeste erstatter.
const autoDupThreshold = 0.75

// ingestFact innlemmer et bekreftet faktum: dublettvakten erstatter automatisk
// en nær-identisk gammel lapp, noden lagres som accepted og speiles til
// retrieval-skuffen.
func (s *Server) ingestFact(tenantID, userID, chatID, typ, title, summary string, vec []float32) error {
	if old, sim, err := s.store.MostSimilarAcceptedFact(tenantID, vec, ""); err == nil && sim >= autoDupThreshold {
		s.log.Info("dublettvakt: erstatter", "gammel", old.Title, "ny", title, "sim", sim)
		s.store.DeleteNode(old.ID, tenantID)
	}
	node, err := s.store.CreateNode(tenantID, store.KnowledgeNode{
		Type: typ, Title: title, Summary: summary,
		Status: "accepted", ChatID: chatID, UserID: userID, Embedding: vec,
	})
	if err != nil {
		return err
	}
	return s.store.SyncFactNote(tenantID, node.ID, title, summary, vec)
}

// extractMarkers er tegn på at brukeren forklarer noe bedriftsinternt (kilden
// til kunnskapsnoder), i motsetning til å stille et spørsmål eller småprate.
var extractMarkers = []string{
	"vi ", "vår", "våre", "hos oss", "hos meg", "internt", "rutine", "pleier",
	"kalles", "betyr", " må ", " skal ", "bruker vi", "vi bruker", "regel",
	"krav", "policy", "prosedyre", "standarden", "alltid", "aldri",
	// Eksplisitte notiser: brukeren BER om at noe huskes — skal alltid inn.
	"husk", "noter", "notér", "merk deg", "lagre dette", "skriv deg bak øret",
}

// worthExtracting gater uttrekk-kallet: kun substansielle meldinger der
// brukeren faktisk forklarer noe bedriftsinternt er verdt et LLM-kall.
// Deterministisk forhåndsfilter = sparer det store flertallet av kallene.
func worthExtracting(question string) bool {
	q := strings.TrimSpace(question)
	low := strings.ToLower(q)
	// Eksplisitt notis: «husk …»/«noter …» er verdt et kall uansett lengde.
	for _, p := range []string{"husk", "noter", "notér", "merk deg"} {
		if strings.HasPrefix(low, p) {
			return len([]rune(q)) >= 12
		}
	}
	if len([]rune(q)) < 40 {
		return false
	}
	for _, m := range extractMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// extractProposals ber en billig modell foreslå 0–3 kunnskaps-noder fra
// utvekslingen — kun forslag, lagring skjer først ved kildens bekreftelse.
func (s *Server) extractProposals(ctx context.Context, tenantID, question, answer string) []extractedNode {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	titles, _ := s.store.NodeTitles(tenantID)
	existing := "(ingen ennå)"
	if len(titles) > 0 {
		existing = strings.Join(titles, "; ")
	}
	userMsg := "Eksisterende noder (ikke dupliser): " + existing +
		"\n\nSamtale:\nBruker: " + question + "\nAI: " + answer

	payload := map[string]any{
		"model": extractModel,
		"messages": []map[string]string{
			{"role": "system", "content": extractSystem},
			{"role": "user", "content": userMsg},
		},
		"temperature":     0.2,
		"max_tokens":      600,
		"response_format": map[string]any{"type": "json_object"},
	}
	body, _ := json.Marshal(payload)
	req, err := s.newUpstreamRequest(ctx, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warn("uttrekk-kall feilet", "err", err)
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return nil
	}

	var parsed struct {
		Nodes []extractedNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(extractJSON(out.Choices[0].Message.Content)), &parsed); err != nil {
		return nil
	}

	kept := make([]extractedNode, 0, len(parsed.Nodes))
	for _, n := range parsed.Nodes {
		n.Title = strings.TrimSpace(n.Title)
		n.Summary = strings.TrimSpace(n.Summary)
		if n.Title == "" || n.Summary == "" {
			continue
		}
		// Hopp over dubletter (tittelmatch) — resten tar dublettvakten ved
		// bekreftelse.
		if _, err := s.store.NodeByTitle(tenantID, n.Title); err == nil {
			continue
		}
		kept = append(kept, n)
	}
	return kept
}

// docExtractSystem destillerer et dokument til strukturert grafkunnskap (G4 i
// docs/KNOWLEDGE.md): prosedyrer, regler, termer og ansvar blir noder som
// kobles til dok-noden. I motsetning til chat-uttrekket er terskler og tall
// ØNSKET her — dokumentet er allerede godkjent som bedriftskunnskap.
const docExtractSystem = "Du får en del av et internt bedriftsdokument. Trekk ut den gjenbrukbare " +
	"STRUKTURERTE kunnskapen: prosedyrer (stegene komprimert, i rekkefølge, i én summary), faste " +
	"regler og terskler (behold tall ordrett), interne fagtermer med betydning, ferdigheter/skills (hvordan noe gjøres hos oss, stegvis), og ansvar " +
	"(hvem-gjør-hva). Hopp over ren prosa uten regel- eller prosessverdi, og alt som allerede står i " +
	"listen over eksisterende noder. Maks 4 per del. Svar KUN med JSON: " +
	"{\"nodes\":[{\"type\":\"prosess|regel|term|entitet\",\"title\":\"kort navn\",\"summary\":\"presis, selvstendig\"}]}"

// runDocExtraction kjøres i bakgrunnen etter dokumentopplasting: oppretter
// dok-noden i grafen og destillerer dokumentet seksjon for seksjon til
// accepted-noder med kant til dokumentet (governance v2: opplastingen er
// bekreftelsen; dublettvakten kjører automatisk).
func (s *Server) runDocExtraction(tenantID, userID, docID, title, summary, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	docVec, err := s.embed(ctx, title+". "+summary)
	if err != nil {
		s.log.Warn("dok-uttrekk: embedding feilet", "err", err)
		return
	}
	if err := s.store.EnsureDocNode(tenantID, docID, title, summary, docVec); err != nil {
		s.log.Warn("dok-uttrekk: dok-node feilet", "err", err)
		return
	}

	created := 0
	for _, section := range chunkText(text) {
		titles, _ := s.store.NodeTitles(tenantID)
		existing := "(ingen ennå)"
		if len(titles) > 0 {
			existing = strings.Join(titles, "; ")
		}
		raw, err := s.llmComplete(ctx, "kunnskap", docExtractSystem,
			"Eksisterende noder (ikke dupliser): "+existing+"\n\nDokument: "+title+"\n\n"+section, 600)
		if err != nil {
			continue
		}
		var parsed struct {
			Nodes []extractedNode `json:"nodes"`
		}
		if json.Unmarshal([]byte(extractJSON(raw)), &parsed) != nil {
			continue
		}
		for _, n := range parsed.Nodes {
			n.Title = strings.TrimSpace(n.Title)
			n.Summary = strings.TrimSpace(n.Summary)
			if n.Title == "" || n.Summary == "" {
				continue
			}
			if _, err := s.store.NodeByTitle(tenantID, n.Title); err == nil {
				continue
			}
			vec, err := s.embed(ctx, n.Title+". "+n.Summary)
			if err != nil {
				continue
			}
			// Governance v2: opplastingen VAR bekreftelsen — rett til accepted,
			// med dublettvakten i forkant.
			if err := s.ingestFact(tenantID, userID, "", n.Type, n.Title, n.Summary, vec); err != nil {
				continue
			}
			if id, err := s.store.NodeByTitle(tenantID, n.Title); err == nil {
				s.store.AddEdge(tenantID, store.KnowledgeEdge{
					FromID: id, ToID: docID, Relation: "definert i",
				})
			}
			created++
		}
	}
	s.log.Info("dok-uttrekk ferdig", "dokument", title, "noder", created)
}

// extractJSON plukker ut det ytterste JSON-objektet fra et modell-svar.
func extractJSON(s string) string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}
