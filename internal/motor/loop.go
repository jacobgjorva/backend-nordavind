package motor

import (
	"context"
	"os"
	"strings"
)

// Arbeidsløkka — motorens eneste kontrollflyt.
//
// FORMEN ER MÅLT. Fire arkitekturer ble kjørt mot ekte Large 3 med 16
// oppgaver hver (docs/ENGINE_MVP_ROUND1_RESULTS.md): den native løkka var
// den ENESTE uten motorfeil og uten protokolltekst i svaret. De tre andre
// la et skjema mellom modellen og oppgaven, og modellen begynte å skrive
// verktøykall som rå tekst i brukersvaret — 11 av 16, og 10 av 16.
//
// Løkka er derfor med vilje kjedelig: kall modellen, utfør det den ba om,
// gjenta til den svarer med tekst. All kvalitetsforskjell ligger i
// metodeteksten (methods.go) og i gulvene, aldri i grener her.
//
// DET LØKKA EIER, og ingenting annet:
//
//	rundetaket · kvotene · rekkefølgen · overlevering ved ukjent verktøy ·
//	å fange tanken · å skrive meldingene tilbake i samtalen
//
// DET LØKKA ALDRI GJØR: leser svarteksten for å avgjøre noe. Hver eneste
// vakt vi har bygget på formulering («finnes ikke», «jeg kan ikke») måtte
// skrotes fordi modellen alltid fant en ny vending. Beslutninger her tas
// på fakta: kom det verktøykall, er kvoten brukt, er runden nummer N.

// Deliverer skriver sluttsvaret. Løkka avslutter turen med å kalle den, så
// leveransen (del 6) kan bygges uavhengig av arbeidet.
type Deliverer interface {
	Deliver(ctx context.Context, turn *Turn, draft string)
}

// Engine er motoren. Alle avhengigheter er kontrakter, så løkka kan kjøres
// helt uten nettverk i test.
type Engine struct {
	Model   ModelCaller
	Tools   ToolRunner
	Out     Emitter
	Deliver Deliverer
	// Log er valgfri; nil er lovlig og gjør motoren stille.
	Log Logger
}

// Logger er den lille flaten motoren trenger av en logger.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

func (e *Engine) logf(warn bool, msg string, args ...any) {
	if e.Log == nil {
		return
	}
	if warn {
		e.Log.Warn(msg, args...)
		return
	}
	e.Log.Info(msg, args...)
}

// upstreamDown er det ærlige svaret når leverandøren ikke svarer. Ingen
// fallback-modell: én modell er et bevisst valg, og en stille degradering
// til noe annet ville skjult driftsproblemet.
const upstreamDown = "Jeg får ikke kontakt med modellen akkurat nå. Prøv igjen om et lite øyeblikk."

// Run kjører hele turen.
//
// payload er forespørselen slik api-laget alt har bygget den (modell,
// meldinger, verktøy); løkka utvider meldingslisten og lar resten stå.
//
// Returnerer handoff=true når turen tilhører en annen løype. Da er
// INGENTING sendt til brukeren, og kalleren kan kjøre legacy urørt.
func (e *Engine) Run(ctx context.Context, payload map[string]any, turn *Turn) (handoff bool) {
	budget := BudgetFor(turn.Method)
	e.logf(false, "motor v6: tur startet", "metode", string(turn.Method),
		"runder", budget.Rounds, "søk", budget.Searches, "hentinger", budget.Fetches)

	for turn.Rounds < budget.Rounds {
		turn.Rounds++

		resp, err := e.Model.Call(ctx, ModelRequest{Payload: payload})
		if err != nil {
			e.logf(true, "motor v6: upstream feilet", "err", err, "runde", turn.Rounds)
			// Første runde: ingenting er levert, og turen er trygg å gi
			// videre. Senere: vi har alt vist arbeid, så vi svarer ærlig
			// her i stedet for å starte alt på nytt i en annen løype.
			if turn.Rounds == 1 {
				return true
			}
			e.Out.Content(upstreamDown)
			e.Out.Done()
			return false
		}
		turn.Usage.PromptTokens += resp.Usage.PromptTokens
		turn.Usage.CompletionTokens += resp.Usage.CompletionTokens

		// Ferdig arbeidet: modellen svarer med tekst i stedet for verktøykall.
		if resp.Done() {
			e.Deliver.Deliver(ctx, turn, resp.Text)
			return false
		}

		// Tanken som fulgte med kallene er turens mest undervurderte
		// ressurs: den ble betalt for og kastet i alle tidligere motorer.
		// Del 4 gjør den til narrasjon og relevansanker; her fanges den.
		if th := resp.Thought(); th != "" {
			turn.Thoughts = append(turn.Thoughts, th)
		}

		if e.runTools(ctx, payload, turn, budget, resp.Calls) {
			return true
		}
	}

	// Rundetaket nådd: skriv fra det som faktisk er hentet. Aldri en tom
	// tur, aldri en intern feiltekst til brukeren.
	e.logf(false, "motor v6: rundetak nådd", "metode", string(turn.Method), "runder", turn.Rounds)
	e.Deliver.Deliver(ctx, turn, "")
	return false
}

// runTools utfører modellens kall og skriver resultatene tilbake i
// samtalen. Returnerer handoff=true når et kall tilhører en annen løype.
func (e *Engine) runTools(ctx context.Context, payload map[string]any, turn *Turn,
	budget Budget, calls []ToolCall) (handoff bool) {

	assistantCalls := make([]any, 0, len(calls))
	toolMsgs := make([]any, 0, len(calls))

	for _, c := range calls {
		res := e.Tools.Run(ctx, c, budget, turn)
		if res.Handoff {
			// Verktøyet har en leveransekontrakt motoren ikke kjenner
			// (compose-kort, widget-blokk, rutine-kvittering). Turen gis
			// videre HEL — vi har ikke sendt noe til brukeren.
			e.logf(false, "motor v6: verktøy utenfor motoren, gir turen videre", "verktøy", c.Name)
			turn.Handoff = true
			return true
		}
		turn.Record(res)
		if len(res.Sources) > 0 {
			e.Out.Sources(res.Sources)
		}

		assistantCalls = append(assistantCalls, map[string]any{
			"id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": c.Args},
		})
		toolMsgs = append(toolMsgs, map[string]any{
			"role": "tool", "tool_call_id": c.ID, "name": c.Name, "content": res.Text,
		})
	}

	msgs, _ := payload["messages"].([]any)
	// Assistentmeldingen bærer tanken videre som innhold. Den er en del av
	// samtalen modellen selv skrev, og å slette den ville gjort neste runde
	// blind for hva forrige runde lette etter.
	msgs = append(msgs, map[string]any{
		"role": "assistant", "content": turn.LastThought(), "tool_calls": assistantCalls,
	})
	payload["messages"] = append(msgs, toolMsgs...)
	return false
}

// --- Systemprompten -------------------------------------------------------

// BuildSystem setter sammen turens systeminstruks: husets faste regler
// (bygget av api-laget som i dag) + metodeteksten + tenk-regelen.
//
// Rekkefølgen er bevisst. Metoden kommer SIST av innholdsreglene fordi den
// er turens mest spesifikke instruks, og tenk-regelen helt til slutt fordi
// den styrer FORMEN på neste melding, ikke innholdet i svaret.
//
// Kun ÉN metodetekst kan komme med — stabling av regler er målt å gjøre
// svarene verre (feedback-no-prompt-stuffing), og klassene er gjensidig
// utelukkende nettopp derfor.
func BuildSystem(base string, method MethodKey, tools bool) string {
	var b strings.Builder
	b.WriteString(base)
	// Tenk-regelen gir bare mening når det finnes verktøy å tenke foran.
	// Uten verktøy ville den bedt modellen skrive et arbeidsnotat til en
	// jobb den ikke kan utføre.
	if tools && thinkFirst {
		b.WriteString(ThinkRule)
	}
	b.WriteString(TextFor(method))
	if tools && !thinkFirst {
		b.WriteString(ThinkRule)
	}
	return b.String()
}

// thinkFirst avgjør om tenk-regelen står foran eller bak metodeteksten.
// MIDLERTIDIG under måling (del 3) — settes til den vinnende rekkefølgen
// og fjernes så. En permanent bryter her ville vært en kodegren for noe
// som skal være avgjort.
var thinkFirst = os.Getenv("V6_THINK_FIRST") != "off"

// ThinkRule ber modellen tenke kort høyt i SAMME svar som verktøykallene.
//
// Probet i to runder mot ekte Large 3 (P1/P4, docs/MOTOR-V6-PROBER.md):
// tanken kom på de komplekse turene og uteble på de trivielle, den var
// norsk og tett nok til å vises som arbeidssteg, og null tilfeller av
// «plan i stedet for handling» — feilen «utfør, aldri annonser» ble
// skrevet for å drepe.
const ThinkRule = " Før verktøykallene: tenk kort høyt i én til tre setninger — hva brukeren egentlig " +
	"trenger, hva du alt vet, og hva du må sjekke. Skriv tanken som vanlig tekst og kall verktøyene " +
	"i SAMME svar. Tanken er arbeidsnotat, ikke svaret."
