// Package motor er assistentmotoren v6 — «metodemotoren».
//
// HVORFOR EGEN PAKKE. v1-v5 vokste inn i api-pakken til agent.go passerte
// 1 900 linjer, og hver ny mekanisme ble en gren til i den samme funksjonen.
// v6 har ingen tilgang til api-pakkens innmat: den ser bare kontraktene i
// denne filen. Det gjør motoren testbar uten nettverk, og gjør det fysisk
// umulig å løse et problem ved å strekke seg inn i en nabomekanisme.
//
// ARBEIDSDELINGEN (docs/MOTOR-V6.md):
//
//	MODELLEN eier mening: hva brukeren egentlig spør om, hvilke verktøy som
//	trengs, hvordan svaret formuleres.
//
//	KODEN eier fakta: kvoter, aggregering, tallkontroll, budsjetter,
//	rekkefølge og garantier. Alt kode gjør, utløses av OBSERVERBARE fakta
//	(radantall, kvotestand, om et tall finnes i kildene) — aldri av hvordan
//	modellen ordla seg. Formuleringsvakter er målt som teknisk gjeld fra
//	første linje: modellen finner alltid en ny vending.
//
// JUSTERING UTEN LAPPING. All adferd som kan trenge endring bor i DATA:
// en ny oppgaveklasse er en rad i metodekatalogen (methods.go), endret
// dybde er et tall i radens budsjett, endret svarform er radens tekst.
// Ingen av delene er en kodegren. Oppstår en ny feilklasse, får den ett
// generelt svar i katalog eller gulv — enkelttilfeller lappes aldri.
package motor

import (
	"context"
	"strings"
)

// --- Kontraktene mot omverdenen -------------------------------------------
//
// Motoren eier ingen nettverkskode. api-pakken implementerer disse med den
// EKSISTERENDE, målte koden (upstream-kall, søk, database, SSE) — v6
// dupliserer ingenting av det.

// ModelCaller kjører én completion og returnerer det modellen produserte.
// Implementasjonen eier transport, leverandørvalg og SSE-parsing.
type ModelCaller interface {
	Call(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

// ModelRequest er én runde inn. Payload er turens fulle forespørsel slik
// api-laget alt bygger den (modell, meldinger, verktøy) — motoren muterer
// meldingslisten og lar resten stå.
type ModelRequest struct {
	Payload map[string]any
}

// ModelResponse er én runde ut.
//
// Thought er prosaen modellen skrev SAMMEN med verktøykallene. Den er
// gull og ble kastet i alle tidligere motorer: v6 bruker den som
// arbeidsnarrasjon til brukeren OG som relevansanker for kilderangeringen
// (docs/MOTOR-V6.md del 5.2). Er Calls tom, er Text sluttsvaret.
type ModelResponse struct {
	Text  string     // sluttsvar (når Calls er tom) eller tanke (når den ikke er det)
	Calls []ToolCall // verktøykall modellen ba om, i rekkefølge
	Usage Usage
}

// Thought gir prosaen som fulgte med verktøykallene, eller tom streng når
// runden var et sluttsvar. Skiller de to bruksmåtene av Text eksplisitt, så
// ingen kallsted trenger å huske betingelsen.
func (r ModelResponse) Thought() string {
	if len(r.Calls) == 0 {
		return ""
	}
	return strings.TrimSpace(r.Text)
}

// Done: modellen er ferdig å arbeide og har levert tekst.
func (r ModelResponse) Done() bool { return len(r.Calls) == 0 }

// ToolCall er ett verktøykall fra modellen, med argumentene som rå JSON.
type ToolCall struct {
	ID   string
	Name string
	Args string
}

// Usage er tokenforbruk for én runde, slik leverandøren rapporterte det.
// Aldri utledet eller estimert av oss.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// ToolRunner utfører ett verktøykall og returnerer resultatet modellen skal
// se. Implementasjonen eier kvotehåndhevelse, cache, aggregering av store
// resultatsett og ærlige feiltekster — motoren stoler aldri på modellen for
// telling eller sannhet om hva som skjedde.
type ToolRunner interface {
	// Run utfører kallet. Ukjent verktøy gir Handoff=true, og motoren gir
	// hele turen videre til den løypa som eier kontrakten (widget, e-post,
	// eksport …) uten å ha levert noe selv.
	Run(ctx context.Context, call ToolCall, budget Budget, turn *Turn) ToolResult
}

// ToolResult er ett utført verktøykall. Text er det modellen får se.
type ToolResult struct {
	Text string
	// Handoff: verktøyet eies ikke av motoren. Turen gis videre urørt.
	Handoff bool
	// Kind klassifiserer kallet for kvoteregnskapet (KindSearch, KindFetch,
	// KindData …). Tellingen skjer i kode, aldri i prompten.
	Kind ToolKind
	// Evidence: resultatet er KILDEGRUNNLAG som svaret kan måles mot.
	// Handlingsverktøy (som viser noe) er ikke evidens.
	Evidence bool
	// Sources er kildene brukeren skal se, hvis kallet hentet noen.
	Sources []Source
}

// ToolKind er verktøyets kostnadsklasse. Budsjettene i metodekatalogen er
// satt per klasse, ikke per verktøynavn — nye verktøy arver en klasse i
// stedet for å trenge egen kvoteregel.
type ToolKind int

const (
	KindOther ToolKind = iota
	KindSearch
	KindFetch
	KindData
	KindShow
)

// Source er én kilde, slik frontend viser den.
type Source struct {
	Title string
	URL   string
}

// Emitter sender til klienten. Motoren skriver aldri SSE-syntaks selv.
type Emitter interface {
	// Content sender synlig svartekst.
	Content(text string)
	// Step sender ett arbeidssteg (narrasjon). kind styrer ikonet.
	Step(text, kind string)
	// Sources sender kildelista.
	Sources(src []Source)
	// Done avslutter strømmen.
	Done()
}

// --- Turens tilstand ------------------------------------------------------

// Turn er alt turen har gjort og hentet. Ren data: ingen metoder som tar
// avgjørelser, så tilstanden aldri blir et sted å gjemme adferd.
type Turn struct {
	// Question er brukerens FAKTISKE spørsmål, fanget ved turstart.
	// Kritisk lærdom fra v3: etter en injisert instruks returnerer
	// «siste brukermelding» VÅR tekst, ikke brukerens — og motoren begynte
	// å vurdere sin egen instruks i stedet for oppgaven.
	Question string
	// Method er klassen turen kjører som. Tom = naken løkke (fail-open).
	Method MethodKey

	// Thoughts er prosaen modellen skrev underveis, i rekkefølge. Siste
	// tanke er relevansankeret for neste henting.
	Thoughts []string
	// Evidence er verktøyresultater som svaret kan måles mot.
	Evidence []string
	// Sources er kildene som skal vises brukeren.
	Sources []Source

	// Forbruk, talt i kode.
	Rounds   int
	Searches int
	Fetches  int
	Usage    Usage

	// UsedTool: turen kalte minst ett verktøy. Skiller «fant ingenting»
	// fra «prøvde ingenting» i den ærlige bunnen.
	UsedTool bool
	// Handoff: turen tilhører en annen løype og er ikke levert her.
	Handoff bool
}

// LastThought er relevansankeret: det modellen sist sa den lette etter.
// Tom streng når turen ikke har tenkt høyt ennå.
func (t *Turn) LastThought() string {
	if len(t.Thoughts) == 0 {
		return ""
	}
	return t.Thoughts[len(t.Thoughts)-1]
}

// Spent teller forbruket av én kostnadsklasse.
func (t *Turn) Spent(k ToolKind) int {
	switch k {
	case KindSearch:
		return t.Searches
	case KindFetch:
		return t.Fetches
	}
	return 0
}

// Record fører ett utført kall inn i regnskapet.
func (t *Turn) Record(r ToolResult) {
	t.UsedTool = true
	switch r.Kind {
	case KindSearch:
		t.Searches++
	case KindFetch:
		t.Fetches++
	}
	if r.Evidence && strings.TrimSpace(r.Text) != "" {
		t.Evidence = append(t.Evidence, r.Text)
	}
	t.Sources = append(t.Sources, r.Sources...)
}
