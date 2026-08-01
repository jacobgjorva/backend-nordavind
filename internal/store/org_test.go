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
	if err := s.SetEmployeeUnits("t1", e.ID, []string{u.ID}); err != nil {
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
	mu, _ := s.EmployeeUnits("t1")
	if len(mu[e.ID]) != 0 {
		t.Fatalf("medlemskapet skulle vært slettet med enheten: %+v", mu)
	}
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

	e1, _ := s.CreateEmployee("t1", Employee{Name: "Eksplisitt", Role: "selger", UserID: uExplicit.ID})
	e2, _ := s.CreateEmployee("t1", Employee{Name: "Epost", Role: "regnskap", Email: "B@X.NO"})
	u2, _ := s.CreateUnit("t1", OrgUnit{Name: "Selskap C"})
	if err := s.SetEmployeeUnits("t1", e1.ID, []string{u.ID, u2.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEmployeeUnits("t1", e2.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}

	sc, err := s.ScopeFor("t1", uExplicit.ID)
	if err != nil || len(sc.UnitIDs) != 2 || !sc.Has(u.ID) || !sc.Has(u2.ID) || sc.Role != "selger" {
		t.Fatalf("eksplisitt kobling m/flergrupper: %+v, err=%v", sc, err)
	}
	sc, err = s.ScopeFor("t1", uEmail.ID)
	if err != nil || len(sc.UnitIDs) != 1 || !sc.Has(u.ID) || sc.Role != "regnskap" {
		t.Fatalf("epost-fallback (case-ufølsom): %+v, err=%v", sc, err)
	}
	sc, err = s.ScopeFor("t1", uNone.ID)
	if err != nil || len(sc.UnitIDs) != 0 || sc.Role != "" {
		t.Fatalf("uten ansattrad skal scope være tom: %+v, err=%v", sc, err)
	}
}
