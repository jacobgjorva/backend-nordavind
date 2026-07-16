package store

import (
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLoginFlow(t *testing.T) {
	s := testStore(t)
	tenant, err := s.CreateTenant("Acme AS")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(tenant.ID, "kari@acme.no", "admin"); err != nil {
		t.Fatal(err)
	}

	code, err := s.CreateLoginCode("kari@acme.no")
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("kode skal være 6 sifre, fikk %q", code)
	}

	token, user, err := s.RedeemCode("kari@acme.no", code)
	if err != nil {
		t.Fatal(err)
	}
	if user.TenantID != tenant.ID {
		t.Fatalf("feil tenant: %s", user.TenantID)
	}

	got, err := s.UserBySession(token)
	if err != nil || got.Email != "kari@acme.no" {
		t.Fatalf("sesjon feilet: %v %v", got, err)
	}

	// Koden er engangs
	if _, _, err := s.RedeemCode("kari@acme.no", code); err != ErrInvalidCode {
		t.Fatalf("gjenbruk skal avvises, fikk %v", err)
	}
}

func TestRedeemWrongCode(t *testing.T) {
	s := testStore(t)
	tenant, _ := s.CreateTenant("Acme AS")
	s.CreateUser(tenant.ID, "ola@acme.no", "member")
	s.CreateLoginCode("ola@acme.no")

	if _, _, err := s.RedeemCode("ola@acme.no", "000000"); err != ErrInvalidCode {
		t.Fatalf("feil kode skal avvises, fikk %v", err)
	}
}
