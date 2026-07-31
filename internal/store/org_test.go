package store

import (
	"path/filepath"
	"testing"
)

func orgStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "org.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Enheter er scope-fundamentet: rundtur + at sletting løsner ansatte i
// stedet for å la dem peke på en død enhet (stille lekkasje-risiko).
func TestUnitsRoundTripAndDetachOnDelete(t *testing.T) {
	s := orgStore(t)
	u, err := s.CreateUnit("t1", OrgUnit{Name: "Selskap A"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.CreateEmployee("t1", Employee{Name: "Kari Nes", Role: "økonomiansvarlig", UnitID: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	units, _ := s.ListUnits("t1")
	if len(units) != 1 || units[0].Name != "Selskap A" {
		t.Fatalf("uventet enhetsliste: %+v", units)
	}
	if err := s.DeleteUnit(u.ID, "t1"); err != nil {
		t.Fatal(err)
	}
	emps, _ := s.ListEmployees("t1")
	if len(emps) != 1 || emps[0].UnitID != "" {
		t.Fatalf("ansatt skulle vært løsnet fra slettet enhet: %+v", emps)
	}
	_ = e
}

// ScopeFor er primitiven spørringsbyggeren (del 2) skal stole på:
// eksplisitt user_id-kobling vinner, e-post er fallback, og en bruker uten
// ansattrad får TOM scope — aldri en gjettet enhet.
func TestScopeForResolvesExplicitThenEmailThenEmpty(t *testing.T) {
	s := orgStore(t)
	u, _ := s.CreateUnit("t1", OrgUnit{Name: "Selskap B"})

	uExplicit, err := s.CreateUser("t1", "a@x.no", "member")
	if err != nil {
		t.Fatal(err)
	}
	uEmail, err := s.CreateUser("t1", "b@x.no", "member")
	if err != nil {
		t.Fatal(err)
	}
	uNone, err := s.CreateUser("t1", "c@x.no", "member")
	if err != nil {
		t.Fatal(err)
	}

	s.CreateEmployee("t1", Employee{Name: "Eksplisitt", Role: "selger", UnitID: u.ID, UserID: uExplicit.ID})
	s.CreateEmployee("t1", Employee{Name: "Epost", Role: "regnskap", UnitID: u.ID, Email: "B@X.NO"})

	sc, err := s.ScopeFor("t1", uExplicit.ID)
	if err != nil || sc.UnitID != u.ID || sc.Role != "selger" {
		t.Fatalf("eksplisitt kobling: %+v, err=%v", sc, err)
	}
	sc, err = s.ScopeFor("t1", uEmail.ID)
	if err != nil || sc.UnitID != u.ID || sc.Role != "regnskap" {
		t.Fatalf("epost-fallback (case-ufølsom): %+v, err=%v", sc, err)
	}
	sc, err = s.ScopeFor("t1", uNone.ID)
	if err != nil || sc.UnitID != "" || sc.Role != "" {
		t.Fatalf("uten ansattrad skal scope være tom: %+v, err=%v", sc, err)
	}
}
