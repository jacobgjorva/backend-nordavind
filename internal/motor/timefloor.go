package motor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Tidsgulvet (2026-08-02). Målt to ganger i prod: «fristen i år» besvart
// med 2024-dato, og en alder sitert fra en gammel kilde («19 år») rett ved
// siden av fødselsdatoen som beviser 21. Modellen papegøyer kildenes
// tidsankre; gulvet eier de to observerbare tilfellene:
//
//  1. ALDER SOM KAN REGNES: står fødselsdato og alder i SAMME svar, er
//     alderen ren aritmetikk — koden regner og retter tallet, og sier fra.
//  2. FERSKHET: et tidsdeiktisk spørsmål («i år», «nå», «hvor gammel»)
//     besvart utelukkende med årstall eldre enn inneværende år får én
//     ærlig merknad — samme mønster som dekningsgulvet, aldri omskriving.
//
// Alt annet (prisendringer, «nyeste versjon» uten årstall) er semantikk
// gulv ikke kan avgjøre — de forblir modellens ansvar og evalens tema.

var (
	birthRe = regexp.MustCompile(`født (?:den )?(\d{1,2})\.? (januar|februar|mars|april|mai|juni|juli|august|september|oktober|november|desember) (\d{4})`)
	ageRe   = regexp.MustCompile(`(\d{1,3}) år`)
	yearRe  = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
)

var monthNum = map[string]time.Month{
	"januar": 1, "februar": 2, "mars": 3, "april": 4, "mai": 5, "juni": 6,
	"juli": 7, "august": 8, "september": 9, "oktober": 10, "november": 11, "desember": 12,
}

// AgeFix retter en alder svaret selv motbeviser. Konservativt: kun når
// svaret har NØYAKTIG én fødselsdato og én alder — flere personer i samme
// svar er utenfor det koden kan bevise.
func AgeFix(answer string, today time.Time) (string, bool) {
	births := birthRe.FindAllStringSubmatch(strings.ToLower(answer), -1)
	ages := ageRe.FindAllStringSubmatch(answer, -1)
	if len(births) != 1 || len(ages) != 1 {
		return answer, false
	}
	day, _ := strconv.Atoi(births[0][1])
	year, _ := strconv.Atoi(births[0][3])
	month := monthNum[births[0][2]]
	if month == 0 || day < 1 || day > 31 || year < 1900 || year > today.Year() {
		return answer, false
	}
	birth := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	age := today.Year() - birth.Year()
	if today.Month() < birth.Month() || (today.Month() == birth.Month() && today.Day() < birth.Day()) {
		age--
	}
	claimed, _ := strconv.Atoi(ages[0][1])
	if claimed == age || age < 0 || age > 120 {
		return answer, false
	}
	fixed := strings.Replace(answer, ages[0][0], fmt.Sprintf("%d år", age), 1)
	return fixed, true
}

// timeDeicticWords: spørsmål som handler om NÅ. Ordliste, ikke semantikk —
// gulv utløses kun på observerbare kjennetegn.
var timeDeicticWords = []string{
	"i år", "i dag", "akkurat nå", "for tiden", "nyeste", "gjeldende",
	"hvor gammel", "hvor gamle", "fristen", "frist i",
}

// TimeDeictic sier om spørsmålet er forankret i inneværende tid. Eksportert:
// førstesøket bruker samme kjennetegn til å legge årstallet på søket.
func TimeDeictic(question string) bool {
	q := strings.ToLower(question)
	for _, w := range timeDeicticWords {
		if strings.Contains(q, w) {
			return true
		}
	}
	return false
}

// StaleNote gir ferskhetsmerknaden: tidsdeiktisk spørsmål, og ALLE årstall
// i svaret er eldre enn inneværende år. Nevner svaret inneværende år, eller
// har det ingen årstall, er det ingenting å merke.
func StaleNote(question, answer string, today time.Time) string {
	if !TimeDeictic(question) {
		return ""
	}
	years := yearRe.FindAllString(answer, -1)
	if len(years) == 0 {
		return ""
	}
	newest := 0
	for _, y := range years {
		if n, _ := strconv.Atoi(y); n > newest {
			newest = n
		}
	}
	if newest >= today.Year() {
		return ""
	}
	return fmt.Sprintf("Merk: årstallene her stammer fra kilder datert %d — verifiser mot %d før du bruker dem.",
		newest, today.Year())
}

// TimeFloor kjører begge grepene i rekkefølge og returnerer svaret pluss
// om noe ble endret (telemetri).
func TimeFloor(question, answer string, today time.Time) (string, bool) {
	out, fixed := AgeFix(answer, today)
	if note := StaleNote(question, out, today); note != "" {
		out = strings.TrimRight(out, " \n") + "\n\n" + note
		fixed = true
	}
	return out, fixed
}
