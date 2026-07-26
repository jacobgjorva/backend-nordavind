package api

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/router"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// Rullerende samtalesammendrag: frontend sender bare de siste meldingene
// (tegnbudsjettet i chatHelpers.ts) — alt eldre ville vært TAPT for modellen.
// Sammendraget fanger det som faller ut av vinduet og injiseres som
// system-kontekst i runAgentLoop, så lange samtaler husker starten uten at
// hver tur betaler for hele historikken.

const (
	// summaryAfterMsgs/Chars: fra når samtalen er stor nok til at noe faller
	// ut av frontend-vinduet (speiler HISTORY_WINDOW=12 / 24k-budsjettet der).
	summaryAfterMsgs  = 12
	summaryAfterChars = 15000
	// summaryKeepTail: de nyeste meldingene ligger alt i vinduet — sammendraget
	// dekker kun det som er ELDRE enn disse.
	summaryKeepTail = 12
	// summaryMaxChars: taket på selve sammendraget (≈300 tokens per tur).
	summaryMaxChars = 1200
)

const chatSummarySystem = "Du komprimerer starten av en samtale mellom en bruker og en assistent " +
	"til et sammendrag på maks 1200 tegn, på norsk. Behold ALT som kan trengs senere: fakta, tall, " +
	"navn, beslutninger, brukerens mål og preferanser, og innholdet i vedlagte dokumenter i kortform. " +
	"Skriv tett og nøkternt, ingen innledning — kun sammendraget."

// summaryInFlight hindrer dobbeltarbeid når meldinger lagres tett.
var summaryInFlight sync.Map // chatID -> struct{}

// maybeUpdateChatSummary sjekker billig (COUNT/SUM) om samtalen har vokst ut
// av frontend-vinduet, og oppdaterer i så fall sammendraget i bakgrunnen.
// Kalles etter at en assistent-melding er lagret.
func (s *Server) maybeUpdateChatSummary(chatID string, user store.User) {
	n, chars, err := s.store.ChatSize(chatID)
	if err != nil || (n <= summaryAfterMsgs && chars <= summaryAfterChars) {
		return
	}
	if _, busy := summaryInFlight.LoadOrStore(chatID, struct{}{}); busy {
		return
	}
	go func() {
		defer summaryInFlight.Delete(chatID)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		s.updateChatSummary(ctx, chatID, user)
	}()
}

// updateChatSummary komprimerer alt eldre enn halen til ett nytt sammendrag.
// Forrige sammendrag er med som grunnlag, så ingenting går tapt selv om de
// eldste meldingene senere slettes. Feil = behold forrige (fail-open).
func (s *Server) updateChatSummary(ctx context.Context, chatID string, user store.User) {
	msgs, err := s.store.ChatMessages(chatID, user.ID)
	if err != nil || len(msgs) <= summaryKeepTail {
		return
	}
	old, _ := s.store.ChatSummary(chatID, user.ID)

	var b strings.Builder
	if old != "" {
		b.WriteString("TIDLIGERE SAMMENDRAG:\n" + old + "\n\n")
	}
	b.WriteString("MELDINGENE SOM SKAL INN I SAMMENDRAGET:\n")
	for _, m := range msgs[:len(msgs)-summaryKeepTail] {
		role := "Bruker"
		if m.Role == "assistant" {
			role = "Assistent"
		}
		text := m.Content
		// Én melding kan bære hele dokumenter — sammendraget trenger essensen,
		// ikke alt: klipp input så selve sammendragskallet holder seg billig.
		if r := []rune(text); len(r) > 6000 {
			text = string(r[:6000]) + " …[klippet]"
		}
		b.WriteString(role + ": " + text + "\n")
	}

	sum, err := s.llmCompleteModel(ctx, router.LightModel, "sammendrag",
		chatSummarySystem, b.String(), 400, user)
	if err != nil || strings.TrimSpace(sum) == "" {
		return
	}
	if r := []rune(sum); len(r) > summaryMaxChars {
		sum = string(r[:summaryMaxChars])
	}
	if err := s.store.SetChatSummary(chatID, user.ID, sum); err != nil {
		s.log.Warn("kunne ikke lagre samtalesammendrag", "chat", chatID, "err", err)
	}
}
