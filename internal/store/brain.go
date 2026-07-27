package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Hjernelaget: entiteter, påstander og prosedyrer. Lappe-skuffen
// (knowledge_notes) står urørt som råtekst for sitering — dette er
// destillatet over den, det som faktisk kan traverseres.

type Entity struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Aliases []string  `json:"aliases,omitempty"`
	Created time.Time `json:"created_at"`
}

type Claim struct {
	ID         string            `json:"id"`
	SubjectID  string            `json:"subject_id"`
	Predicate  string            `json:"predicate"`
	ObjectID   string            `json:"object_id,omitempty"`
	ObjectText string            `json:"object_text,omitempty"`
	Qualifiers map[string]string `json:"qualifiers,omitempty"`
	Confidence float64           `json:"confidence"`
	ValidFrom  time.Time         `json:"valid_from"`
	ValidTo    *time.Time        `json:"valid_to,omitempty"`
	SourceKind string            `json:"source_kind"`
	SourceID   string            `json:"source_id"`
	StatedBy   string            `json:"stated_by"`
}

func (s *Store) migrateBrain() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS entities (
			id         TEXT PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			kind       TEXT NOT NULL,
			name       TEXT NOT NULL,
			aliases    TEXT NOT NULL DEFAULT '[]',
			embedding  TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen  TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_entities_tenant ON entities (tenant_id, kind);

		CREATE TABLE IF NOT EXISTS claims (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			subject_id  TEXT NOT NULL,
			predicate   TEXT NOT NULL,
			object_id   TEXT NOT NULL DEFAULT '',
			object_text TEXT NOT NULL DEFAULT '',
			qualifiers  TEXT NOT NULL DEFAULT '{}',
			confidence  REAL NOT NULL DEFAULT 1,
			valid_from  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			valid_to    TIMESTAMP,
			source_kind TEXT NOT NULL DEFAULT '',
			source_id   TEXT NOT NULL DEFAULT '',
			stated_by   TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_claims_subject ON claims (tenant_id, subject_id);
		CREATE INDEX IF NOT EXISTS idx_claims_object ON claims (tenant_id, object_id);

		CREATE TABLE IF NOT EXISTS procedures (
			id          TEXT PRIMARY KEY,
			tenant_id   TEXT NOT NULL,
			name        TEXT NOT NULL,
			trigger_txt TEXT NOT NULL DEFAULT '',
			steps       TEXT NOT NULL DEFAULT '[]',
			valid_from  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			valid_to    TIMESTAMP,
			source_kind TEXT NOT NULL DEFAULT '',
			source_id   TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_procedures_tenant ON procedures (tenant_id);
	`)
	return err
}

// UpsertEntity lager entiteten eller føyer nye aliaser til en som finnes.
// Navnematch er case-uavhengig og dekker aliaser — «sjefen» og «Kari H.»
// skal aldri bli to noder.
func (s *Store) UpsertEntity(tenantID string, e Entity, embedding []float32) (Entity, error) {
	if found, err := s.EntityByName(tenantID, e.Name); err == nil && found.ID != "" {
		if len(e.Aliases) > 0 {
			merged := append([]string{}, found.Aliases...)
			for _, a := range e.Aliases {
				if !containsFold(merged, a) && !strings.EqualFold(a, found.Name) {
					merged = append(merged, a)
				}
			}
			raw, _ := json.Marshal(merged)
			if _, err := s.db.Exec(
				`UPDATE entities SET aliases = ?, last_seen = CURRENT_TIMESTAMP
				 WHERE id = ? AND tenant_id = ?`, string(raw), found.ID, tenantID); err != nil {
				return found, err
			}
			found.Aliases = merged
		}
		return found, nil
	}
	id, err := newID()
	if err != nil {
		return Entity{}, err
	}
	e.ID = id
	e.Created = time.Now()
	aliases, _ := json.Marshal(e.Aliases)
	_, err = s.db.Exec(
		`INSERT INTO entities (id, tenant_id, kind, name, aliases, embedding, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		e.ID, tenantID, e.Kind, e.Name, string(aliases), marshalVec(embedding),
	)
	return e, err
}

// EntityByName slår opp på navn ELLER alias, uavhengig av store bokstaver.
func (s *Store) EntityByName(tenantID, name string) (Entity, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return Entity{}, nil
	}
	rows, err := s.db.Query(
		`SELECT id, kind, name, aliases, created_at FROM entities
		 WHERE tenant_id = ? AND (lower(name) = ? OR lower(aliases) LIKE ?)`,
		tenantID, n, "%\""+n+"\"%",
	)
	if err != nil {
		return Entity{}, err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return Entity{}, err
		}
		// LIKE kan treffe en delstreng i et annet alias — bekreft eksakt.
		if strings.EqualFold(e.Name, name) || containsFold(e.Aliases, name) {
			return e, nil
		}
	}
	return Entity{}, rows.Err()
}

// EntitiesByKind lister entiteter av en type (til grafen og mønsterjobben).
func (s *Store) EntitiesByKind(tenantID, kind string) ([]Entity, error) {
	rows, err := s.db.Query(
		`SELECT id, kind, name, aliases, created_at FROM entities
		 WHERE tenant_id = ? AND (? = '' OR kind = ?) ORDER BY name`,
		tenantID, kind, kind,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEntity(rows *sql.Rows) (Entity, error) {
	var e Entity
	var aliases string
	if err := rows.Scan(&e.ID, &e.Kind, &e.Name, &aliases, &e.Created); err != nil {
		return e, err
	}
	json.Unmarshal([]byte(aliases), &e.Aliases)
	return e, nil
}

// marshalVec lagrer en embedding som JSON — samme form som lappene bruker.
func marshalVec(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// AddClaim lagrer en påstand. Motstrid håndteres av kalleren (SupersedeClaim)
// — lageret sier aldri hva som er sant, det lagrer bare det det får.
func (s *Store) AddClaim(tenantID string, c Claim) (Claim, error) {
	id, err := newID()
	if err != nil {
		return c, err
	}
	c.ID = id
	if c.ValidFrom.IsZero() {
		c.ValidFrom = time.Now()
	}
	q, _ := json.Marshal(c.Qualifiers)
	_, err = s.db.Exec(
		`INSERT INTO claims (id, tenant_id, subject_id, predicate, object_id, object_text,
		 qualifiers, confidence, valid_from, valid_to, source_kind, source_id, stated_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, tenantID, c.SubjectID, c.Predicate, c.ObjectID, c.ObjectText,
		string(q), c.Confidence, c.ValidFrom, c.ValidTo, c.SourceKind, c.SourceID, c.StatedBy,
	)
	return c, err
}

// SupersedeClaims lukker gjeldende påstander med samme subjekt og predikat.
// Slik holdes tidslinjen entydig: historikken beholdes, men bare én er sann
// nå.
func (s *Store) SupersedeClaims(tenantID, subjectID, predicate string, at time.Time) (int64, error) {
	res, err := s.db.Exec(
		`UPDATE claims SET valid_to = ?
		 WHERE tenant_id = ? AND subject_id = ? AND predicate = ? AND valid_to IS NULL`,
		at, tenantID, subjectID, predicate,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClaimsAbout gir påstandene der entiteten er subjekt ELLER objekt. Bare de
// som gjelder nå, med mindre history er satt — traversering skal aldri dra
// med seg utdaterte fakta uten at noen har bedt om det.
func (s *Store) ClaimsAbout(tenantID string, ids []string, history bool) ([]Claim, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{tenantID}
	for _, id := range ids {
		args = append(args, id)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	where := ""
	if !history {
		where = " AND valid_to IS NULL"
	}
	rows, err := s.db.Query(
		`SELECT id, subject_id, predicate, object_id, object_text, qualifiers,
		        confidence, valid_from, valid_to, source_kind, source_id, stated_by
		 FROM claims
		 WHERE tenant_id = ? AND (subject_id IN (`+ph+`) OR object_id IN (`+ph+`))`+where+`
		 ORDER BY confidence DESC, valid_from DESC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Claim
	for rows.Next() {
		var c Claim
		var q string
		var validTo sql.NullTime
		if err := rows.Scan(&c.ID, &c.SubjectID, &c.Predicate, &c.ObjectID, &c.ObjectText,
			&q, &c.Confidence, &c.ValidFrom, &validTo, &c.SourceKind, &c.SourceID, &c.StatedBy); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(q), &c.Qualifiers)
		if validTo.Valid {
			t := validTo.Time
			c.ValidTo = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveProcedure lagrer en prosedyre med stegene i rekkefølge.
func (s *Store) SaveProcedure(tenantID, name, trigger string, steps any, sourceKind, sourceID string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(steps)
	_, err = s.db.Exec(
		`INSERT INTO procedures (id, tenant_id, name, trigger_txt, steps, source_kind, source_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, tenantID, name, trigger, string(raw), sourceKind, sourceID,
	)
	return id, err
}

// Procedure henter én prosedyre med stegene.
func (s *Store) Procedure(tenantID, id string) (string, string, []byte, error) {
	var name, trigger, steps string
	err := s.db.QueryRow(
		`SELECT name, trigger_txt, steps FROM procedures
		 WHERE tenant_id = ? AND id = ? AND valid_to IS NULL`, tenantID, id,
	).Scan(&name, &trigger, &steps)
	return name, trigger, []byte(steps), err
}
