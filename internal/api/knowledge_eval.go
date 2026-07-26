package api

import "context"

// EvalKnowledge kjører kunnskapshentingen for en gitt tenant og spørring —
// eksportert KUN for eval-harnesset (cmd/knowledge-eval), som måler presisjon,
// injiserte tegn og latens mot fasit (G6 i docs/KNOWLEDGE.md). Ingen
// produksjonskode kaller denne.
func (s *Server) EvalKnowledge(ctx context.Context, tenantID, query string) string {
	return s.knowledgeContext(ctx, tenantID, query)
}
