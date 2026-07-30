package api

import (
	"context"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Fullstendig forbruks-telling: HVERT LLM-kall skal innom én av funksjonene
// under — også dommere, embeddings, klassifisering og bakgrunnsjobber.
// /forbruk-panelet var før bare agent-loopen; alt utenom (intent, faktadommer,
// workjudge, agentplan, rutiner, dokument-jobber) gikk urapportert
// (~10 % av prompt-tokens og over halvparten av completion-tokens i måling
// 26.07.2026). Fasit mot leverandørfakturaen krever at alle kall telles.

// countLLM logger ett LLM-kall med bruker hentet fra request-konteksten.
// Brukes fra alle kallsteder som lever i en innlogget forespørsel.
func (s *Server) countLLM(ctx context.Context, model, source string, promptTokens, completionTokens int) {
	tenantID, userID := "", ""
	if user, ok := ctx.Value(userKey).(store.User); ok {
		tenantID, userID = user.TenantID, user.ID
	}
	s.countLLMFor(tenantID, userID, model, source, promptTokens, completionTokens)
}

// countLLMFor logger ett LLM-kall med eksplisitt eier — for bakgrunnsjobber
// (rutiner, planbygg) der forespørsels-konteksten ikke bærer brukeren.
func (s *Server) countLLMFor(tenantID, userID, model, source string, promptTokens, completionTokens int) {
	if promptTokens == 0 && completionTokens == 0 {
		return
	}
	err := s.store.InsertUsage(store.UsageEvent{
		TenantID:         tenantID,
		UserID:           userID,
		Model:            model,
		Source:           source,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostUSD:          s.pricing.cost(model, promptTokens, completionTokens),
	})
	if err != nil {
		s.log.Warn("kunne ikke lagre usage", "source", source, "err", err)
	}
}
