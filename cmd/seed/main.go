// Seed oppretter en tenant med en admin-bruker:
//
//	go run ./cmd/seed -tenant "Acme AS" -email kari@acme.no
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jacobgjorva/backend-nordavind/internal/config"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

func main() {
	tenantName := flag.String("tenant", "", "navn på bedriften")
	email := flag.String("email", "", "e-post til admin-brukeren")
	flag.Parse()
	if *tenantName == "" || *email == "" {
		fmt.Fprintln(os.Stderr, "bruk: seed -tenant <navn> -email <e-post>")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer st.Close()

	tenant, err := st.CreateTenant(*tenantName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	user, err := st.CreateUser(tenant.ID, *email, "admin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("tenant %s (%s), admin %s\n", tenant.Name, tenant.ID, user.Email)
}
