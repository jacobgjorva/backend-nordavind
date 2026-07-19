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

const extractSystem = "Du trekker ut BEDRIFTS-INTERN operasjonell innsikt som BRUKEREN har lært " +
	"AI-en i denne samtalen — ting AI-en umulig kunne visst fra før. \n" +
	"KILDEN er kun det BRUKEREN forklarer/oppgir om hvordan akkurat denne bedriften jobber: interne " +
	"rutiner, egne fagtermer, hvem-gjør-hva, faste regler og krav som er spesifikke for dem. \n" +
	"LAKMUSTEST: «Ville en kompetent AI allerede visst dette uten å få det fortalt av denne bedriften?» " +
	"Hvis JA → utelat. Ta ALDRI med allmennkunnskap, definisjoner, det AI-en selv svarte, eller noe " +
	"som kom fra nettsøk/treningsdata. \n" +
	"UTELUKK OGSÅ: forretningsstrategi, konkrete økonomiske tall (omsetning, priser, lønn), " +
	"personopplysninger, navn på ansatte, engangsting, meninger, sensitivt. MEN behold terskler/" +
	"grenser som er en fast del av en rutine (f.eks. «ordrer over X må godkjennes»). Ved tvil: utelat. \n" +
	"De fleste samtaler gir INGEN noder — returner da tom liste. Lag aldri dubletter av det som " +
	"allerede finnes (listen under). Maks 3 noder. \n" +
	"Svar KUN med JSON: {\"nodes\":[{\"type\":\"term|prosess|regel|entitet\",\"title\":\"kort navn\"," +
	"\"summary\":\"én presis setning\",\"relations\":[{\"to\":\"tittel på eksisterende eller ny node\"," +
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

// handleExtractKnowledge starter uttrekk i bakgrunnen etter en utveksling.
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
	// Fyr og glem: brukeren skal ikke vente.
	go s.runExtraction(user.TenantID, user.ID, req.ChatID, req.Question, req.Answer)
	w.WriteHeader(http.StatusAccepted)
}

// extractMarkers er tegn på at brukeren forklarer noe bedriftsinternt (kilden
// til kunnskapsnoder), i motsetning til å stille et spørsmål eller småprate.
var extractMarkers = []string{
	"vi ", "vår", "våre", "hos oss", "hos meg", "internt", "rutine", "pleier",
	"kalles", "betyr", " må ", " skal ", "bruker vi", "vi bruker", "regel",
	"krav", "policy", "prosedyre", "standarden", "alltid", "aldri",
}

// worthExtracting gater uttrekk-kallet: kun substansielle meldinger der
// brukeren faktisk forklarer noe bedriftsinternt er verdt et LLM-kall.
// Deterministisk forhåndsfilter = sparer det store flertallet av kallene.
func worthExtracting(question string) bool {
	q := strings.TrimSpace(question)
	if len([]rune(q)) < 40 {
		return false
	}
	low := strings.ToLower(q)
	for _, m := range extractMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// runExtraction ber en billig modell foreslå 0–3 pending-noder og lagrer dem
// med embeddings og relasjoner.
func (s *Server) runExtraction(tenantID, userID, chatID, question, answer string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warn("uttrekk-kall feilet", "err", err)
		return
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
		return
	}

	var parsed struct {
		Nodes []extractedNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(extractJSON(out.Choices[0].Message.Content)), &parsed); err != nil {
		return
	}

	for _, n := range parsed.Nodes {
		n.Title = strings.TrimSpace(n.Title)
		n.Summary = strings.TrimSpace(n.Summary)
		if n.Title == "" || n.Summary == "" {
			continue
		}
		// Hopp over dubletter (tittelmatch).
		if _, err := s.store.NodeByTitle(tenantID, n.Title); err == nil {
			continue
		}
		vec, err := s.embed(ctx, n.Title+". "+n.Summary)
		if err != nil {
			continue
		}
		node, err := s.store.CreateNode(tenantID, store.KnowledgeNode{
			Type: n.Type, Title: n.Title, Summary: n.Summary,
			Status: "pending", ChatID: chatID, UserID: userID, Embedding: vec,
		})
		if err != nil {
			continue
		}
		// Koble relasjoner til eksisterende noder (nye titler kobles når de finnes).
		for _, rel := range n.Relations {
			toID, err := s.store.NodeByTitle(tenantID, strings.TrimSpace(rel.To))
			if err != nil {
				continue
			}
			s.store.AddEdge(tenantID, store.KnowledgeEdge{
				FromID: node.ID, ToID: toID, Relation: strings.TrimSpace(rel.Relation),
			})
		}
		s.log.Info("kunnskapsnode foreslått", "tittel", n.Title)
	}
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
