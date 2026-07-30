package api

import (
	"context"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Inngangen til motor v6: én funksjon som setter sammen delene og kjører
// turen.
//
// Kontrakten mot agent-løkka er den samme som legacy-motoren hadde: kjør
// hvis du kan, returner took=false hvis ikke, og da har du ikke sendt noe
// til brukeren. Det gjør bytet trygt — feiler v6 på noen måte, tar den
// gamle løypa turen uten at brukeren merker det.

// runMotorV6 kjører turen med metodemotoren.
//
// took=false betyr at turen IKKE er levert her: enten er motoren av, eller
// flyten eies av en annen løype, eller et verktøy krevde overlevering.
// Ingenting er sendt til klienten i de tilfellene.
func (s *Server) runMotorV6(ctx context.Context, full map[string]any, emit func(string),
	dbCtx *dbToolCtx, narr *narrator, promptTokens, completionTokens *int) (string, bool) {

	if !s.motorOn() {
		return "", false
	}
	flowKey := motorFlowKey(full)
	if !motor.Takes(full, flowKey) {
		return "", false
	}

	// Brukerens FAKTISKE spørsmål, fanget FØR noen injiserte instrukser.
	// Kritisk lærdom fra v3: etter en injisert nudge returnerer «siste
	// brukermelding» VÅR tekst, og motoren begynte å vurdere sin egen
	// instruks i stedet for oppgaven.
	turn := &motor.Turn{
		Question: lastUserText(full),
		Method:   motorMethod(full),
	}

	state := &motorTurnState{}
	floors := &motorFloors{
		s: s, emit: emit, narr: narr, state: state,
		question:    turn.Question,
		multiSource: dbCtx != nil && len(dbCtx.conns) > 1,
	}
	tools := &motorTools{s: s, dbCtx: dbCtx, narr: narr, emit: emit, state: state, floors: floors}
	out := apiEmitter{emit: emit, narr: narr}

	delivery := &motor.Delivery{
		Out:      out,
		Floors:   floors,
		Writer:   &motorWriter{s: s, full: full, promptTokens: promptTokens, completionTokens: completionTokens},
		Facts:    motorFacts{},
		Verifier: motorVerifier{},
		Log:      s.log,
		Limit:    motorAnswerLimit(turn.Method, full),
		EmptyExplain: func() string {
			return floors.dbExplain()
		},
	}

	engine := &motor.Engine{
		Model:   apiModel{s: s},
		Tools:   tools,
		Out:     out,
		Deliver: delivery,
		Log:     s.log,
	}

	s.log.Info("motor v6: tur", "flyt", flowKey, "metode", string(turn.Method))
	if handoff := engine.Run(ctx, full, turn); handoff {
		return "", false
	}

	// Tokenregnskapet fra arbeidsrundene føres på forespørselen. Skriverens
	// forbruk er alt lagt til i motorWriter.
	if promptTokens != nil {
		*promptTokens += turn.Usage.PromptTokens
	}
	if completionTokens != nil {
		*completionTokens += turn.Usage.CompletionTokens
	}
	return delivery.Answer, true
}
