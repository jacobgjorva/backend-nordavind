package api

import (
	"context"

	"github.com/jacobgjorva/backend-nordavind/internal/knowledge"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Adaptere for kunnskapsgraf v2 (KNOWLEDGE=v2): api-laget eier nettverk og
// scope-oppslag, pakken eier utvalget. v1-veien består urørt til del 5.

type v2Embedder struct{ s *Server }

func (e v2Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return e.s.embedCached(ctx, text)
}

type v2Source struct {
	s      *Server
	tenant string
	scope  store.Scope
	userID string
}

func (src v2Source) Vector(ctx context.Context, vec []float32, k int) ([]knowledge.Candidate, error) {
	notes, err := src.s.store.ScopedVectorCandidates(src.tenant, src.scope, src.userID, vec, k)
	if err != nil {
		return nil, err
	}
	return toCandidates(notes), nil
}

func (src v2Source) Keyword(ctx context.Context, tokens []string, k int) ([]knowledge.Candidate, error) {
	notes, err := src.s.store.ScopedKeywordCandidates(src.tenant, src.scope, src.userID, tokens, k)
	if err != nil {
		return nil, err
	}
	return toCandidates(notes), nil
}

func (src v2Source) Neighbors(ctx context.Context, seeds []string, k int) ([]knowledge.Candidate, error) {
	notes, err := src.s.store.ScopedNeighbors(src.tenant, src.scope, src.userID, seeds, k)
	if err != nil {
		return nil, err
	}
	return toCandidates(notes), nil
}

func toCandidates(notes []store.ScopedNote) []knowledge.Candidate {
	out := make([]knowledge.Candidate, len(notes))
	for i, n := range notes {
		out[i] = knowledge.Candidate{
			ID: n.ID, Title: n.Title, Text: n.Text, Extra: n.Context,
			Sim: n.Sim, Source: n.Source, SourceID: n.SourceID,
		}
	}
	return out
}

// knowledgeV2For er v2-varianten av knowledgeFor: scope-oppslag + Context.
func (s *Server) knowledgeV2For(ctx context.Context, full map[string]any) string {
	user, ok := ctx.Value(userKey).(store.User)
	if !ok || user.TenantID == "" {
		return ""
	}
	sc, _ := s.store.ScopeFor(user.TenantID, user.ID)
	src := v2Source{s: s, tenant: user.TenantID, scope: sc, userID: user.ID}
	block, err := knowledge.Context(ctx, v2Embedder{s: s}, src, knowledge.Request{
		Question: stripAttachments(lastUserText(full)),
	})
	if err != nil || block.Text == "" {
		return ""
	}
	go s.store.RecordNoteHits(block.Used)
	s.log.Info("kunnskap v2", "linjer", block.Lines, "tegn", len(block.Text))
	return block.Text
}
