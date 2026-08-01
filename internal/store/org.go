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
// Tomt UnitIDs = brukeren tilhører ingen gruppe og ser kun tenant-vid
// kunnskap. En bruker kan tilhøre FLERE grupper (Jacobs krav 2026-08-01);
// tenant-vid kunnskap ser alle uansett — gruppene LEGGER TIL synlighet.
type Scope struct {
	UnitIDs []string
	Role    string
}

// Has sier om scopet inneholder enheten.
func (sc Scope) Has(unitID string) bool {
	for _, id := range sc.UnitIDs {
		if id == unitID {
			return true
		}
	}
	return false
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
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS employee_units (
			employee_id TEXT NOT NULL,
			unit_id     TEXT NOT NULL,
			tenant_id   TEXT NOT NULL,
			PRIMARY KEY (employee_id, unit_id)
		);
		CREATE INDEX IF NOT EXISTS idx_emp_units_tenant ON employee_units (tenant_id, unit_id);
	`); err != nil {
		return err
	}
	// Engangsmigrering: gamle én-enhets-koblinger blir medlemskap.
	_, err := s.db.Exec(`
		INSERT INTO employee_units (employee_id, unit_id, tenant_id)
		SELECT id, unit_id, tenant_id FROM employees WHERE unit_id != ''
		ON CONFLICT DO NOTHING`)
	return err
}

// SetEmployeeUnits erstatter en ansatts gruppemedlemskap.
func (s *Store) SetEmployeeUnits(tenantID, employeeID string, unitIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM employee_units WHERE employee_id = ? AND tenant_id = ?`,
		employeeID, tenantID); err != nil {
		return err
	}
	for _, uid := range unitIDs {
		if uid == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO employee_units (employee_id, unit_id, tenant_id) VALUES (?, ?, ?)
			 ON CONFLICT DO NOTHING`, employeeID, uid, tenantID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EmployeeUnits henter medlemskapene per ansatt for tenanten.
func (s *Store) EmployeeUnits(tenantID string) (map[string][]string, error) {
	rows, err := s.db.Query(
		`SELECT employee_id, unit_id FROM employee_units WHERE tenant_id = ?`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var eid, uid string
		if err := rows.Scan(&eid, &uid); err != nil {
			return nil, err
		}
		out[eid] = append(out[eid], uid)
	}
	return out, rows.Err()
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
	if _, err = s.db.Exec(
		`UPDATE employees SET unit_id = '' WHERE unit_id = ? AND tenant_id = ?`, id, tenantID); err != nil {
		return err
	}
	_, err = s.db.Exec(
		`DELETE FROM employee_units WHERE unit_id = ? AND tenant_id = ?`, id, tenantID)
	return err
}

// ScopeFor utleder brukerens synlighetsposisjon: først eksplisitt kobling
// (employees.user_id), så e-postmatch mot brukerkontoen. Ingen ansattrad =
// tom scope (kun tenant-vid kunnskap) — trygg default. Feil gir alltid
// MINDRE synlighet, aldri mer.
func (s *Store) ScopeFor(tenantID, userID string) (Scope, error) {
	var empID string
	var sc Scope
	err := s.db.QueryRow(
		`SELECT id, role FROM employees WHERE tenant_id = ? AND user_id = ?`,
		tenantID, userID).Scan(&empID, &sc.Role)
	if err != nil {
		err = s.db.QueryRow(
			`SELECT e.id, e.role FROM employees e
			 JOIN users u ON lower(u.email) = lower(e.email) AND u.tenant_id = e.tenant_id
			 WHERE e.tenant_id = ? AND u.id = ? AND e.email != ''`,
			tenantID, userID).Scan(&empID, &sc.Role)
		if err != nil {
			return Scope{}, nil
		}
	}
	rows, err := s.db.Query(
		`SELECT unit_id FROM employee_units WHERE tenant_id = ? AND employee_id = ?`,
		tenantID, empID)
	if err != nil {
		return sc, nil
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err == nil {
			sc.UnitIDs = append(sc.UnitIDs, uid)
		}
	}
	return sc, nil
}
