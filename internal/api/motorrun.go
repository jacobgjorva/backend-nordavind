package api

import (
	"strconv"
	"strings"
	"time"

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

	// Metodeteksten og tenk-regelen INN i systemprompten. Uten dette er v6
	// bare budsjetter: metoden velges, kvoten brukes — men fremgangsmåten
	// når aldri modellen. Rekkefølgen (tenk-regel FØR metodetekst) er målt
	// og eies av BuildSystem.
	if extra := motor.BuildSystem("", turn.Method, motorHasTools(full)); extra != "" {
		injectSystem(full, extra)
	}

	// KODE-EID FØRSTESØK på oppslag (2026-08-02): modellen omskriver
	// søkeord den ikke kjenner — «sombr» ble søkt som «SOM Building Oslo»
	// og «gjennomsnittlig høyde på en sombrero», med metodetekst som sa
	// ordrett søk. Formulering kan ikke eies av prompt; koden søker
	// brukerens egne ord FØR modellen får ordet, og resultatet ligger i
	// konteksten uansett hva modellen finner på etterpå. Kun oppslag: der
	// ER spørsmålet søkestrengen. Modellens egne søk kommer i tillegg,
	// innenfor samme budsjett som før.
	var prefetchRefs []sourceRef
	if turn.Method == motor.MethodLookup {
		if q := strings.TrimSpace(stripAttachments(turn.Question)); q != "" {
			// Tidsdeiktiske oppslag («i år», «hvor gammel») får årstallet på
			// søket — ferske kilder inn er bedre enn merknader ut.
			if motor.TimeDeictic(q) {
				q += " " + strconv.Itoa(time.Now().Year())
			}
			if res, refs := s.runWebSearch(ctx, q); res != "" && res != "Søket ga ingen resultater." {
				injectSystem(full, "Søkeresultater for brukerens egne ord «"+q+"» (hentet automatisk):\n"+res)
				// Førstesøket ER verktøybevis: uten dette regnet tallkontrollen
				// et prefetch-belagt svar som hukommelse.
				turn.Evidence = append(turn.Evidence, res)
				prefetchRefs = refs
				s.log.Info("motor v6: førstesøk", "query", q, "kilder", len(refs))
			}
		}
	}

	state := &motorTurnState{}
	floors := &motorFloors{
		s: s, emit: emit, narr: narr, state: state,
		question:    turn.Question,
		multiSource: dbCtx != nil && len(dbCtx.conns) > 1,
	}
	tools := &motorTools{s: s, dbCtx: dbCtx, narr: narr, emit: emit, state: state, floors: floors}
	out := apiEmitter{emit: emit, narr: narr}

	convo := recentTurn(full)
	delivery := &motor.Delivery{
		Out:      out,
		Floors:   floors,
		Writer:   &motorWriter{s: s, full: full, promptTokens: promptTokens, completionTokens: completionTokens},
		Facts:    motorFacts{},
		Verifier: motorVerifier{},
		Form:     motorForm{},
		Compress: &motorCompress{s: s, promptTokens: promptTokens, completionTokens: completionTokens},
		Log:      s.log,
		Limit:    motorAnswerLimit(turn.Method, full),
		Convo:    convo,
		Reground: (&motorReground{s: s, promptTokens: promptTokens, completionTokens: completionTokens}).Reground,
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
		Recheck: func(t *motor.Turn, draft string) string {
			return memoryRecheck(t, draft, convo)
		},
	}

	// Førstesøkets kilder inn i kilde-pillen — de ER turens grunnlag.
	if len(prefetchRefs) > 0 {
		src := make([]motor.Source, 0, len(prefetchRefs))
		for _, r := range prefetchRefs {
			src = append(src, motor.Source{Title: r.Title, URL: r.URL})
		}
		out.Sources(src)
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
