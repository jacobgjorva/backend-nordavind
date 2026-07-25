package api

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Deterministisk steg 1 i connect_database-oppskriften: chatten svarer i kode
// med credential-skjemaet — modellen er aldri involvert, og passord etterspørres
// aldri i chat. Kjente felter parses ut av meldingen som ren prefill.

var (
	// scheme://user[:pass]@host[:port]/db — vanlig connection string.
	connStrRe = regexp.MustCompile(`(?i)\b(postgres(?:ql)?|mysql|sqlserver|mssql)://(?:([^:@/\s]+)(?::[^@\s]*)?@)?([^:/@\s]+)(?::(\d+))?(?:/([^?\s]+))?`)
	hostRe    = regexp.MustCompile(`(?i)\b(?:host|server)[:=\s]+([a-z0-9._-]+)`)
	portRe    = regexp.MustCompile(`(?i)\bport[:=\s]+(\d+)`)
	driverRe  = regexp.MustCompile(`(?i)\b(postgres|postgresql|mysql|mssql|sql\s*server)\b`)
	// hostnavn nevnt løst i teksten, f.eks. «db.kunde.no 5432».
	looseHostRe = regexp.MustCompile(`\b([a-z0-9-]+(?:\.[a-z0-9-]+){1,}\.[a-z]{2,})\b`)
)

func normDriver(s string) string {
	switch strings.ToLower(strings.Join(strings.Fields(s), "")) {
	case "postgres", "postgresql":
		return "postgres"
	case "mysql":
		return "mysql"
	case "mssql", "sqlserver":
		return "mssql"
	}
	return ""
}

// credentialBlock bygger chat-svaret: kort setning + ```credential```-blokk
// med feltene vi klarte å lese ut av meldingen (aldri passord).
func credentialBlock(msg string) string {
	spec := map[string]any{}
	if m := connStrRe.FindStringSubmatch(msg); m != nil {
		if d := normDriver(m[1]); d != "" {
			spec["driver"] = d
		}
		if m[2] != "" {
			spec["user"] = m[2]
		}
		if m[3] != "" {
			spec["host"] = m[3]
		}
		if m[4] != "" {
			if p, err := strconv.Atoi(m[4]); err == nil {
				spec["port"] = p
			}
		}
		if m[5] != "" {
			spec["database"] = m[5]
		}
	} else {
		if m := driverRe.FindStringSubmatch(msg); m != nil {
			if d := normDriver(m[1]); d != "" {
				spec["driver"] = d
			}
		}
		if m := hostRe.FindStringSubmatch(msg); m != nil {
			spec["host"] = m[1]
		} else if m := looseHostRe.FindStringSubmatch(strings.ToLower(msg)); m != nil {
			spec["host"] = m[1]
		}
		if m := portRe.FindStringSubmatch(msg); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil {
				spec["port"] = p
			}
		}
	}
	body, _ := json.Marshal(spec)
	return "La oss koble til databasen — fyll inn her, så testes og lagres alt automatisk. " +
		"Passordet går kryptert utenom chatten. Lurer du på noe underveis, bare spør.\n" +
		"```credential\n" + string(body) + "\n```"
}
