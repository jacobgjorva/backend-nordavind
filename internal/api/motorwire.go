package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/motor"
)

// Koblingen mellom api-pakken og motor v6.
//
// Motoren eier ingen nettverkskode; denne fila implementerer kontraktene
// dens med den EKSISTERENDE, målte koden — upstream-kallet, SSE-parsingen
// og narratoren. Ingenting duplisereres: adapterne er tynne, og all logikk
// blir stående der den alt er verifisert.
//
// v6 er AV med mindre ENGINE=v6. Med flagget av skal payloaden og
// oppførselen være byte-identisk med i dag (testet i motorwire_test.go).

// motorOn: kjører denne serveren metodemotoren?
func (s *Server) motorOn() bool { return s.cfg.Engine == "v6" }

// --- ModelCaller ----------------------------------------------------------

// apiModel kjører én completion gjennom serverens vanlige upstream-løype.
type apiModel struct{ s *Server }

func (m apiModel) Call(ctx context.Context, req motor.ModelRequest) (motor.ModelResponse, error) {
	body, err := json.Marshal(req.Payload)
	if err != nil {
		return motor.ModelResponse{}, err
	}
	httpReq, err := m.s.newUpstreamRequest(ctx, strings.NewReader(string(body)))
	if err != nil {
		return motor.ModelResponse{}, err
	}
	resp, err := m.s.client.Do(httpReq)
	if err != nil {
		return motor.ModelResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.s.log.Warn("motor v6: upstream ikke OK", "status", resp.StatusCode)
	}
	// Bufret runde: motoren avgjør selv hva som vises. Tanken skal bli
	// arbeidssteg, ikke svartekst, så ingenting strømmes rått her.
	calls, usage, content := m.s.relayRound(resp, func(string) {}, false, nil)

	out := motor.ModelResponse{
		Text:  content,
		Usage: motor.Usage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens},
	}
	// relayRound gir kallene i et indeksert kart; motoren vil ha dem i
	// stabil rekkefølge, så indeksen bestemmer.
	for i := 0; i < len(calls)+16 && len(out.Calls) < len(calls); i++ {
		if c, ok := calls[i]; ok {
			out.Calls = append(out.Calls, motor.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args.String()})
		}
	}
	return out, nil
}

// --- Emitter --------------------------------------------------------------

// apiEmitter sender til klienten med serverens eksisterende SSE-format.
type apiEmitter struct {
	emit func(string)
	narr *narrator
}

func (e apiEmitter) Content(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	e.emit(contentSSE(text))
}

// Step går gjennom narratoren, så dedupe, variantrotasjon og steg-typen
// eies ett sted (narrate.go).
func (e apiEmitter) Step(text, kind string) { e.narr.say(text, kind) }

func (e apiEmitter) Sources(src []motor.Source) {
	if len(src) == 0 {
		return
	}
	refs := make([]sourceRef, 0, len(src))
	for _, s := range src {
		refs = append(refs, sourceRef{Title: s.Title, URL: s.URL})
	}
	meta, err := json.Marshal(map[string]any{"nordavind_sources": refs})
	if err != nil {
		return
	}
	e.emit("data: " + string(meta))
}

func (e apiEmitter) Done() { e.emit("data: [DONE]") }

// --- ToolRunner -----------------------------------------------------------

// toolKinds er verktøyenes kostnadsklasse. Nye verktøy arver en klasse her
// i stedet for å trenge sin egen kvoteregel i løkka.
var toolKinds = map[string]motor.ToolKind{
	"web_search":     motor.KindSearch,
	"fetch_url":      motor.KindFetch,
	"query_database": motor.KindData,
	"show_table":     motor.KindShow,
}

// evidenceTools: resultatet er KILDEGRUNNLAG svaret kan måles mot.
// Handlingsverktøy (som viser noe) teller aldri som kilde.
var evidenceTools = map[string]bool{
	"web_search": true, "fetch_url": true, "query_database": true,
	"m365_search": true, "m365_read": true, "mail_search": true, "mail_read": true,
}

// motorToolKind gir kostnadsklassen for et verktøynavn.
func motorToolKind(name string) motor.ToolKind {
	if k, ok := toolKinds[name]; ok {
		return k
	}
	return motor.KindOther
}

// quotaSpent er kodens ærlige beskjed når budsjettet er brukt. Modellen får
// aldri i oppgave å telle selv — den søkte 11 ganger på én tur da den fikk
// prøve (målt i prod).
const quotaSpent = "Kvoten for denne meldingen er brukt opp. Svar brukeren med det du " +
	"allerede har funnet, og si ærlig hva du ikke rakk å sjekke."
