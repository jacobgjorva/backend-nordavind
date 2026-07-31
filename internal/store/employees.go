package store

import "time"

// Employee er en person i bedriften AI-en kan foreslå å kontakte når den selv
// ikke kommer videre. Registeret er per tenant.
type Employee struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`        // stilling/tittel
	Description string    `json:"description"` // funksjon/ansvar — hva personen kan hjelpe med
	Email       string    `json:"email"`
	UnitID      string    `json:"unit_id"` // org-enheten (kunnskaps-scope); tom = ingen enhet
	UserID      string    `json:"user_id"` // kobling til plattformbrukeren, settes i onboarding
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) migrateEmployees() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS employees (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			name        TEXT NOT NULL,
			role        TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			email       TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_employees_tenant ON employees (tenant_id, name);
	`)
	return err
}

// ListEmployees returnerer tenantens ansatte, alfabetisk.
func (s *Store) ListEmployees(tenantID string) ([]Employee, error) {
	rows, err := s.db.Query(
		`SELECT id, name, role, description, email, unit_id, user_id, created_at
		 FROM employees WHERE tenant_id = ? ORDER BY lower(name)`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Employee
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.ID, &e.Name, &e.Role, &e.Description, &e.Email, &e.UnitID, &e.UserID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CreateEmployee legger til en ansatt og returnerer den lagrede raden.
func (s *Store) CreateEmployee(tenantID string, e Employee) (Employee, error) {
	id, err := newID()
	if err != nil {
		return e, err
	}
	e.ID = id
	e.CreatedAt = time.Now()
	_, err = s.db.Exec(
		`INSERT INTO employees (id, tenant_id, name, role, description, email, unit_id, user_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, tenantID, e.Name, e.Role, e.Description, e.Email, e.UnitID, e.UserID,
	)
	return e, err
}

// UpdateEmployee endrer en ansatt.
func (s *Store) UpdateEmployee(id, tenantID string, e Employee) error {
	res, err := s.db.Exec(
		`UPDATE employees SET name = ?, role = ?, description = ?, email = ?, unit_id = ?, user_id = ?
		 WHERE id = ? AND tenant_id = ?`,
		e.Name, e.Role, e.Description, e.Email, e.UnitID, e.UserID, id, tenantID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEmployee fjerner en ansatt.
func (s *Store) DeleteEmployee(id, tenantID string) error {
	res, err := s.db.Exec(`DELETE FROM employees WHERE id = ? AND tenant_id = ?`, id, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
