package api

import (
	"context"

	"github.com/jacobgjorva/backend-nordavind/internal/knowledge"
)

// EvalKnowledge kjører kunnskapshentingen for en gitt tenant og spørring —
// eksportert KUN for eval-harnesset (cmd/knowledge-eval), som måler presisjon,
// injiserte tegn og latens mot fasit (G6 i docs/KNOWLEDGE.md). Ingen
// produksjonskode kaller denne.
func (s *Server) EvalKnowledge(ctx context.Context, tenantID, query string) string {
	return s.knowledgeContext(ctx, tenantID, query)
}

// EvalKnowledgeV2 er v2-porten (KNOWLEDGE=v2-veien) for cmd/knowledge-eval:
// samme utvalgskode som prod, tom scope (eval-tenanten har ingen enheter).
func (s *Server) EvalKnowledgeV2(ctx context.Context, tenantID, query string) string {
	src := v2Source{s: s, tenant: tenantID}
	block, err := knowledge.Context(ctx, v2Embedder{s: s}, src, knowledge.Request{Question: query})
	if err != nil {
		return ""
	}
	return block.Text
}
