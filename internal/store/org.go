package store

import "time"

// Org-modellen (KUNNSKAP-V2-BLUEPRINT del 1): enheter og ansattes kobling
// til dem. Dette er RYGGRADEN for kunnskaps-scope — et dokument scopet til
// en enhet er usynlig for alle utenfor den. Modellen er bevisst minimal:
// enheter (flate eller med forelder), og to nye felt på ansatte (enhet +
// plattformbruker). Roller er ansattes eksisterende Role-felt — ingen egen
// rolletabell før noe faktisk trenger den.
//
// VIKTIG DEFAULT: null org-data betyr at alt er tenant-synlig. Org beriker
// og aktiverer scope — den er aldri en forutsetning for verdi.

// OrgUnit er en enhet: datterselskap, avdeling eller team.
type OrgUnit struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ParentID  string    `json:"parent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Scope er en brukers synlighetsposisjon, utledet av ansatt-koblingen.
// Tom UnitID = brukeren tilhører ingen enhet og ser kun tenant-vid kunnskap.
type Scope struct {
	UnitID string
	Role   string
}

func (s *Store) migrateOrg() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS org_units (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			name       TEXT NOT NULL,
			parent_id  TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_org_units_tenant ON org_units (tenant_id, name);
	`); err != nil {
		return err
	}
	// Idempotente kolonnetillegg: SQLite har ikke ADD COLUMN IF NOT EXISTS,
	// så duplikatfeil er forventet og ufarlig ved andre gangs oppstart.
	for _, stmt := range []string{
		`ALTER TABLE employees ADD COLUMN unit_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE employees ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`,
	} {
		s.db.Exec(stmt)
	}
	return nil
}

// ListUnits returnerer tenantens enheter, alfabetisk.
func (s *Store) ListUnits(tenantID string) ([]OrgUnit, error) {
	rows, err := s.db.Query(
		`SELECT id, name, parent_id, created_at FROM org_units
		 WHERE tenant_id = ? ORDER BY lower(name)`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgUnit
	for rows.Next() {
		var u OrgUnit
		if err := rows.Scan(&u.ID, &u.Name, &u.ParentID, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUnit oppretter en enhet.
func (s *Store) CreateUnit(tenantID string, u OrgUnit) (OrgUnit, error) {
	id, err := newID()
	if err != nil {
		return u, err
	}
	u.ID, u.CreatedAt = id, time.Now()
	_, err = s.db.Exec(
		`INSERT INTO org_units (id, tenant_id, name, parent_id) VALUES (?, ?, ?, ?)`,
		u.ID, tenantID, u.Name, u.ParentID)
	return u, err
}

// UpdateUnit endrer navn/forelder.
func (s *Store) UpdateUnit(id, tenantID string, u OrgUnit) error {
	res, err := s.db.Exec(
		`UPDATE org_units SET name = ?, parent_id = ? WHERE id = ? AND tenant_id = ?`,
		u.Name, u.ParentID, id, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUnit fjerner enheten og løsner ansatte som pekte på den — kunnskap
// scopet til enheten blir utilgjengelig for andre enn admin til den
// re-scopes (bevisst: heller for stramt enn en stille lekkasje).
func (s *Store) DeleteUnit(id, tenantID string) error {
	res, err := s.db.Exec(`DELETE FROM org_units WHERE id = ? AND tenant_id = ?`, id, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.Exec(
		`UPDATE employees SET unit_id = '' WHERE unit_id = ? AND tenant_id = ?`, id, tenantID)
	return err
}

// ScopeFor utleder brukerens synlighetsposisjon: først eksplisitt kobling
// (employees.user_id), så e-postmatch mot brukerkontoen. Ingen ansattrad =
// tom scope (kun tenant-vid kunnskap) — trygg default.
func (s *Store) ScopeFor(tenantID, userID string) (Scope, error) {
	var sc Scope
	err := s.db.QueryRow(
		`SELECT unit_id, role FROM employees WHERE tenant_id = ? AND user_id = ?`,
		tenantID, userID).Scan(&sc.UnitID, &sc.Role)
	if err == nil {
		return sc, nil
	}
	err = s.db.QueryRow(
		`SELECT e.unit_id, e.role FROM employees e
		 JOIN users u ON lower(u.email) = lower(e.email) AND u.tenant_id = e.tenant_id
		 WHERE e.tenant_id = ? AND u.id = ? AND e.email != ''`,
		tenantID, userID).Scan(&sc.UnitID, &sc.Role)
	if err != nil {
		return Scope{}, nil
	}
	return sc, nil
}
