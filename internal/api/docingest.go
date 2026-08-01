package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// docExtractModel: prosedyre-uttrekkets modell. Liten er ønsket (kost +
// fart); PROBES mot Large på fixture-dokumenter før valget festes — se
// kjøring 3 i KUNNSKAP-V2-MAALINGER.md.
const docExtractModel = "mistral-small-2603"

// Dokument-inngest v2 (KUNNSKAP-V2 del 3): scope-arv ved opplasting og
// prosedyre/skill-uttrekk som HENTBARE lapper. Aktiv kun med KNOWLEDGE=v2;
// v1-veien (runDocExtraction → grafnoder) består urørt til del 5.

// resolveDocScope oversetter opplasterens valg til lagrings-scope.
// «unit:<id>» peker på ÉN av brukerens grupper og VALIDERES mot
// medlemskapet — å be om en fremmed gruppe gir tenant, aldri lekkasje.
// «private» finnes for utkast o.l. Tomt valg = arv: brukerens gruppe hvis
// hen har nøyaktig én, ellers hele firmaet (aldri en gjettet gruppe).
func (s *Server) resolveDocScope(tenantID, userID, requested string) string {
	switch {
	case requested == "tenant":
		return store.ScopeTenant
	case requested == "private":
		return store.UserScope(userID)
	case strings.HasPrefix(requested, "unit:"):
		uid := strings.TrimPrefix(requested, "unit:")
		if sc, _ := s.store.ScopeFor(tenantID, userID); sc.Has(uid) {
			return store.UnitScope(uid)
		}
		return store.ScopeTenant
	case requested == "unit":
		if sc, _ := s.store.ScopeFor(tenantID, userID); len(sc.UnitIDs) == 1 {
			return store.UnitScope(sc.UnitIDs[0])
		}
		return store.ScopeTenant
	default:
		if sc, _ := s.store.ScopeFor(tenantID, userID); len(sc.UnitIDs) == 1 {
			return store.UnitScope(sc.UnitIDs[0])
		}
		return store.ScopeTenant
	}
}

// procExtractSystem ber om PROSEDYRER OG SKILLS — fremgangsmåter med steg og
// rekkefølge. Instruksjoner, aldri eksempler (motorens promptregel gjelder
// også her). Små fakta dekkes av dokument-bitene; dette uttrekket skal kun
// fange «hvordan gjør jeg X»-kunnskapen der rekkefølgen ER innholdet.
const procExtractSystem = "Du leser et internt bedriftsdokument og trekker ut PROSEDYRER: " +
	"fremgangsmåter med steg i rekkefølge (hvordan noe gjøres hos denne bedriften). " +
	"Ta KUN med prosedyrer dokumentet faktisk beskriver. Hvert steg må stå i dokumentet — " +
	"skriv aldri steg dokumentet ikke nevner. Beskriver dokumentet ingen trinnvis " +
	"fremgangsmåte, svar {\"procedures\":[]} — de fleste dokumenter har ingen. " +
	"Svar KUN med JSON: {\"procedures\":[{\"name\":\"…\",\"trigger\":\"når den brukes\"," +
	"\"steps\":[\"steg 1 …\",\"steg 2 …\"]}]}"

type procProposal struct {
	Procedures []struct {
		Name    string   `json:"name"`
		Trigger string   `json:"trigger"`
		Steps   []string `json:"steps"`
	} `json:"procedures"`
}

// runDocIngestV2 kjører prosedyre-uttrekket over dokumentet og lagrer hver
// prosedyre som en hentbar lapp (source_type=procedure) med dokumentets
// scope og en kant til dok-noden — så relevans-porten finner den direkte og
// kantutvidelsen finner den via dokumentet.
func (s *Server) runDocIngestV2(tenantID, userID, docID, title, scope, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var found int
	for _, section := range chunkText(text) {
		user := store.User{ID: userID, TenantID: tenantID}
		out, err := s.llmCompleteModel(ctx, docExtractModel, "doc_ingest", procExtractSystem, section, 900, user)
		if err != nil {
			s.log.Warn("dok-inngest v2: uttrekk feilet", "err", err)
			continue
		}
		var p procProposal
		if err := json.Unmarshal([]byte(jsonBody(out)), &p); err != nil {
			continue
		}
		for _, proc := range p.Procedures {
			if proc.Name == "" || len(proc.Steps) < 2 {
				continue // en prosedyre uten steg er ikke en prosedyre
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Prosedyre: %s", proc.Name)
			if proc.Trigger != "" {
				fmt.Fprintf(&b, " (brukes: %s)", proc.Trigger)
			}
			for i, step := range proc.Steps {
				fmt.Fprintf(&b, "\n  %d. %s", i+1, step)
			}
			body := b.String()
			vec, err := s.embed(ctx, body)
			if err != nil {
				continue
			}
			noteID, err := s.store.AddProcedureNote(tenantID, docID, proc.Name, body, scope, vec)
			if err != nil {
				s.log.Warn("dok-inngest v2: lagring feilet", "err", err)
				continue
			}
			s.store.AddEdge(tenantID, store.KnowledgeEdge{
				FromID: noteID, ToID: docID, Relation: "definert i",
			})
			found++
		}
	}
	if found > 0 {
		s.log.Info("dok-inngest v2: prosedyrer lagret", "dokument", title, "antall", found)
	}
}

// jsonBody skreller bort eventuelle kodegjerder rundt modellens JSON.
func jsonBody(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return s[i : j+1]
		}
	}
	return s
}
