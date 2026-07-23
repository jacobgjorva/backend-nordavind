package api

import (
	"strings"
	"testing"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

func ctxWith(conns map[string][]string) *dbToolCtx {
	t := &dbToolCtx{conns: map[string]dbConn{}}
	for id, tables := range conns {
		t.conns[id] = dbConn{conn: store.Connection{Name: id}, allowed: tables}
	}
	return t
}

func TestResolveConn(t *testing.T) {
	two := ctxWith(map[string][]string{
		"kunder": {"orders", "customers"},
		"neon":   {"events", "metrics"},
	})

	// Riktig id oppgitt.
	if _, id, ok, _ := two.resolveConn("kunder", "SELECT * FROM orders"); !ok || id != "kunder" {
		t.Errorf("forventet kunder, fikk %q ok=%v", id, ok)
	}
	// Feil id, men tabellen finnes bare i én → auto-ruting.
	if _, id, ok, _ := two.resolveConn("neon", "SELECT COUNT(*) FROM orders"); !ok || id != "kunder" {
		t.Errorf("auto-ruting feilet: id=%q ok=%v", id, ok)
	}
	// Tom id, unik tabell → ruter.
	if _, id, ok, _ := two.resolveConn("", "SELECT * FROM events"); !ok || id != "neon" {
		t.Errorf("tom id ruting feilet: id=%q ok=%v", id, ok)
	}
	// Ukjent tabell i alle → feil med liste.
	if _, _, ok, msg := two.resolveConn("", "SELECT * FROM nope"); ok || !strings.Contains(msg, "Tilgjengelige databaser") {
		t.Errorf("forventet feil med liste, ok=%v msg=%q", ok, msg)
	}

	// Én kobling: godta selv om tabellen ikke er kjent (SafeQuery gir presis feil).
	one := ctxWith(map[string][]string{"kunder": {"orders"}})
	if _, id, ok, _ := one.resolveConn("feil-id", "SELECT * FROM whatever"); !ok || id != "kunder" {
		t.Errorf("enkeltkobling-fallback feilet: id=%q ok=%v", id, ok)
	}
}
