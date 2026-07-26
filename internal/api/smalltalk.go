package api

import (
	"math/rand"
	"regexp"
	"strings"
)

// Deterministiske småprat-svar: takk og hilsener besvares i KODE, momentant
// og med personlighet — modellens «Vel»/«Velkommen»-varianter var stusslige.
// Fyrer KUN på rene småprat-meldinger (kort, uten spørsmål eller oppgave).

var (
	thanksRe = regexp.MustCompile(`(?i)^(tusen |mange )?takk( skal du ha| for hjelpen| så mye| i ?dag| for nå)?[!. 🙏😊❤️]*$|^(supert|perfekt|topp|nydelig|flott)[,!. ]*takk[!. ]*$`)
	helloRe  = regexp.MustCompile(`(?i)^(hei+|heisann|hallo+|halla|god (morgen|dag|kveld)|morn|yo)[!. 👋]*$`)
	byeRe    = regexp.MustCompile(`(?i)^(ha det( bra)?|god helg|god natt|vi snakkes|takk for i dag|det var alt( for nå)?)[!. 👋]*$`)
)

var thanksReplies = []string{
	"Alltid en glede å hjelpe.",
	"Det var en fornøyelse.",
	"Et skritt av gangen ;)",
	"Takk selv du, stilig oppgave.",
	"Bare hyggelig — det er sånt jeg liker.",
	"Når som helst.",
}

var helloReplies = []string{
	"Hei! Hva skal vi få til i dag?",
	"Heisann — klar når du er.",
	"Hei! Fyr løs.",
	"God dag! Hva står på planen?",
}

var byeReplies = []string{
	"Vi snakkes — jeg er her når du trenger meg.",
	"Ha det bra! Dette var gøy.",
	"God vakt. Jeg holder stillingen.",
	"Snakkes! Alt ligger klart til neste gang.",
}

// cannedSmalltalk gir et ferdig svar for ren småprat, ellers "".
func cannedSmalltalk(msg string) string {
	m := strings.TrimSpace(msg)
	if len([]rune(m)) > 30 {
		return ""
	}
	pick := func(list []string) string { return list[rand.Intn(len(list))] }
	switch {
	case thanksRe.MatchString(m):
		return pick(thanksReplies)
	case helloRe.MatchString(m):
		return pick(helloReplies)
	case byeRe.MatchString(m):
		return pick(byeReplies)
	}
	return ""
}
