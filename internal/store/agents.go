package store

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Agent er en automatisert prosedyre en bruker har satt opp. Den kjører en
// oppgave mot en tilkobling på en fast frekvens.
// Standard er kun lesetilgang; skrivetilgang må settes eksplisitt.
type Agent struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Task            string     `json:"task"`
	ConnectionID    string     `json:"connection_id"`
	ScheduleLabel   string     `json:"schedule_label"`   // menneskelig, f.eks. "hver dag kl 08:00"
	IntervalSeconds int        `json:"interval_seconds"` // hvor ofte den kjøres
	RunTime         string     `json:"run_time"`         // HH:MM for dag/uke-intervaller
	DailyTokenLimit int        `json:"daily_token_limit"`
	WriteAccess     bool       `json:"write_access"`
	Enabled         bool       `json:"enabled"`
	CreatedAt       time.Time  `json:"created_at"`
	ChatID          string     `json:"chat_id"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`

	// Internt for scheduleren (ikke eksponert i JSON til klient).
	TenantID string `json:"-"`
	UserID   string `json:"-"`
}

func (s *Store) migrateAgents() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS agents (
			id                TEXT PRIMARY KEY,
			tenant_id         TEXT NOT NULL,
			user_id           TEXT NOT NULL,
			name              TEXT NOT NULL,
			task              TEXT NOT NULL,
			connection_id     TEXT NOT NULL DEFAULT '',
			schedule_label    TEXT NOT NULL DEFAULT '',
			interval_seconds  INTEGER NOT NULL DEFAULT 86400,
			run_time          TEXT NOT NULL DEFAULT '',
			daily_token_limit INTEGER NOT NULL DEFAULT 0,
			write_access      INTEGER NOT NULL DEFAULT 0,
			enabled           INTEGER NOT NULL DEFAULT 1,
			created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			next_run_at       TIMESTAMP,
			last_run_at       TIMESTAMP,
			chat_id           TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_agents_user ON agents (user_id, created_at);
		CREATE TABLE IF NOT EXISTS agent_runs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id    TEXT NOT NULL,
			started_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			status      TEXT NOT NULL,
			output      TEXT NOT NULL DEFAULT '',
			tokens_used INTEGER NOT NULL DEFAULT 0,
			error       TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_runs_agent ON agent_runs (agent_id, started_at);
	`); err != nil {
		return err
	}
	// Bakoverkompatibelt: legg til nye kolonner på eldre agents-tabeller.
	for _, col := range []string{
		`ALTER TABLE agents ADD COLUMN run_time TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN next_run_at TIMESTAMP`,
		`ALTER TABLE agents ADD COLUMN last_run_at TIMESTAMP`,
		`ALTER TABLE agents ADD COLUMN chat_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	// Indeks på next_run_at først etter at kolonnen finnes.
	_, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_agents_due ON agents (enabled, next_run_at)`,
	)
	return err
}

// NextRun beregner neste kjøretidspunkt. For dag/uke-intervaller med et
// klokkeslett justeres det til riktig tid på døgnet; ellers now + intervall.
func NextRun(now time.Time, intervalSeconds int, runTime string) time.Time {
	step := time.Duration(intervalSeconds) * time.Second
	if runTime != "" && intervalSeconds >= 86400 {
		parts := strings.SplitN(runTime, ":", 2)
		if len(parts) == 2 {
			hh, e1 := strconv.Atoi(parts[0])
			mm, e2 := strconv.Atoi(parts[1])
			if e1 == nil && e2 == nil {
				cand := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
				for !cand.After(now) {
					cand = cand.Add(step)
				}
				return cand
			}
		}
	}
	return now.Add(step)
}

// CreateAgent lagrer en ny agent, oppretter en egen chat for output og
// setter første kjøretidspunkt.
func (s *Store) CreateAgent(tenantID, userID string, a Agent) (Agent, error) {
	a.ID = newID()
	a.CreatedAt = time.Now()
	a.Enabled = true
	next := NextRun(a.CreatedAt, a.IntervalSeconds, a.RunTime)
	a.NextRunAt = &next

	// Egen chat for agentens output, koblet begge veier.
	a.ChatID = newID()
	if _, err := s.db.Exec(
		`INSERT INTO chats (id, tenant_id, user_id, title, agent_id) VALUES (?, ?, ?, ?, ?)`,
		a.ChatID, tenantID, userID, a.Name, a.ID,
	); err != nil {
		return a, err
	}

	_, err := s.db.Exec(
		`INSERT INTO agents
			(id, tenant_id, user_id, name, task, connection_id, schedule_label,
			 interval_seconds, run_time, daily_token_limit, write_access, enabled, next_run_at, chat_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		a.ID, tenantID, userID, a.Name, a.Task, a.ConnectionID, a.ScheduleLabel,
		a.IntervalSeconds, a.RunTime, a.DailyTokenLimit, boolToInt(a.WriteAccess), next, a.ChatID,
	)
	return a, err
}

// ListAgents returnerer brukerens agenter, nyest først.
func (s *Store) ListAgents(userID string) ([]Agent, error) {
	rows, err := s.db.Query(
		`SELECT id, name, task, connection_id, schedule_label, interval_seconds,
		        run_time, daily_token_limit, write_access, enabled, created_at,
		        next_run_at, last_run_at, chat_id
		 FROM agents WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		var write, enabled int
		if err := rows.Scan(&a.ID, &a.Name, &a.Task, &a.ConnectionID, &a.ScheduleLabel,
			&a.IntervalSeconds, &a.RunTime, &a.DailyTokenLimit, &write, &enabled,
			&a.CreatedAt, &a.NextRunAt, &a.LastRunAt, &a.ChatID); err != nil {
			return nil, err
		}
		a.WriteAccess = write != 0
		a.Enabled = enabled != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// DueAgents returnerer aktiverte agenter som skal kjøres nå.
func (s *Store) DueAgents(now time.Time) ([]Agent, error) {
	rows, err := s.db.Query(
		`SELECT id, tenant_id, user_id, name, task, connection_id, schedule_label,
		        interval_seconds, run_time, daily_token_limit, write_access, next_run_at, chat_id
		 FROM agents
		 WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
		 ORDER BY next_run_at`,
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		var write int
		if err := rows.Scan(&a.ID, &a.TenantID, &a.UserID, &a.Name, &a.Task,
			&a.ConnectionID, &a.ScheduleLabel, &a.IntervalSeconds, &a.RunTime,
			&a.DailyTokenLimit, &write, &a.NextRunAt, &a.ChatID); err != nil {
			return nil, err
		}
		a.WriteAccess = write != 0
		a.Enabled = true
		out = append(out, a)
	}
	return out, rows.Err()
}

// RescheduleAgent setter last_run_at og beregner neste kjøretidspunkt.
func (s *Store) RescheduleAgent(a Agent, ranAt time.Time) error {
	next := NextRun(ranAt, a.IntervalSeconds, a.RunTime)
	_, err := s.db.Exec(
		`UPDATE agents SET last_run_at = ?, next_run_at = ? WHERE id = ?`,
		ranAt, next, a.ID,
	)
	return err
}

// AgentRun er én logget kjøring av en agent.
type AgentRun struct {
	Status     string
	Output     string
	TokensUsed int
	Error      string
}

// RecordRun logger resultatet av en agent-kjøring.
func (s *Store) RecordRun(agentID string, r AgentRun) error {
	_, err := s.db.Exec(
		`INSERT INTO agent_runs (agent_id, status, output, tokens_used, error)
		 VALUES (?, ?, ?, ?, ?)`,
		agentID, r.Status, r.Output, r.TokensUsed, r.Error,
	)
	return err
}

// AgentTokensToday summerer tokens agenten har brukt siden midnatt (for
// å håndheve daglig grense).
func (s *Store) AgentTokensToday(agentID string, since time.Time) (int, error) {
	var n sql.NullInt64
	err := s.db.QueryRow(
		`SELECT SUM(tokens_used) FROM agent_runs WHERE agent_id = ? AND started_at >= ?`,
		agentID, since,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n.Int64), nil
}

// UpdateAgentConfig oppdaterer en agents konfigurasjon og planlegger neste
// kjøring på nytt. Chattens tittel følger agentnavnet.
func (s *Store) UpdateAgentConfig(id, userID string, a Agent) error {
	if err := s.agentOwned(id, userID); err != nil {
		return err
	}
	next := NextRun(time.Now(), a.IntervalSeconds, a.RunTime)
	if _, err := s.db.Exec(
		`UPDATE agents SET name=?, task=?, connection_id=?, schedule_label=?,
		        interval_seconds=?, run_time=?, daily_token_limit=?, write_access=?, next_run_at=?
		 WHERE id=? AND user_id=?`,
		a.Name, a.Task, a.ConnectionID, a.ScheduleLabel, a.IntervalSeconds,
		a.RunTime, a.DailyTokenLimit, boolToInt(a.WriteAccess), next, id, userID,
	); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE chats SET title=? WHERE agent_id=?`, a.Name, id)
	return err
}

// SetAgentEnabled setter agenten på pause (false) eller aktiv (true).
func (s *Store) SetAgentEnabled(agentID, userID string, enabled bool) error {
	res, err := s.db.Exec(
		`UPDATE agents SET enabled = ? WHERE id = ? AND user_id = ?`,
		boolToInt(enabled), agentID, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAgent henter en agent brukeren eier (for redigering via chatten).
func (s *Store) GetAgent(id, userID string) (Agent, error) {
	var a Agent
	var write, enabled int
	err := s.db.QueryRow(
		`SELECT id, name, task, connection_id, schedule_label, interval_seconds,
		        run_time, daily_token_limit, write_access, enabled, created_at, chat_id
		 FROM agents WHERE id = ? AND user_id = ?`,
		id, userID,
	).Scan(&a.ID, &a.Name, &a.Task, &a.ConnectionID, &a.ScheduleLabel,
		&a.IntervalSeconds, &a.RunTime, &a.DailyTokenLimit, &write, &enabled,
		&a.CreatedAt, &a.ChatID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.WriteAccess = write != 0
	a.Enabled = enabled != 0
	return a, err
}

// AgentByChat henter agenten som eier en chat (for pause-knappen i UI-et).
func (s *Store) AgentByChat(chatID, userID string) (Agent, error) {
	var a Agent
	var write, enabled int
	err := s.db.QueryRow(
		`SELECT id, name, task, connection_id, schedule_label, interval_seconds,
		        run_time, daily_token_limit, write_access, enabled, created_at,
		        next_run_at, last_run_at, chat_id
		 FROM agents WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	).Scan(&a.ID, &a.Name, &a.Task, &a.ConnectionID, &a.ScheduleLabel,
		&a.IntervalSeconds, &a.RunTime, &a.DailyTokenLimit, &write, &enabled,
		&a.CreatedAt, &a.NextRunAt, &a.LastRunAt, &a.ChatID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.WriteAccess = write != 0
	a.Enabled = enabled != 0
	return a, err
}

// DeleteAgent fjerner en agent brukeren eier, med tilhørende chat og logg.
func (s *Store) DeleteAgent(agentID, userID string) error {
	var chatID string
	err := s.db.QueryRow(
		`SELECT chat_id FROM agents WHERE id = ? AND user_id = ?`, agentID, userID,
	).Scan(&chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if chatID != "" {
		s.db.Exec(`DELETE FROM chat_messages WHERE chat_id = ?`, chatID)
		s.db.Exec(`DELETE FROM chats WHERE id = ?`, chatID)
	}
	s.db.Exec(`DELETE FROM agent_runs WHERE agent_id = ?`, agentID)
	_, err = s.db.Exec(`DELETE FROM agents WHERE id = ? AND user_id = ?`, agentID, userID)
	return err
}

// agentOwned sjekker at agenten tilhører brukeren.
func (s *Store) agentOwned(agentID, userID string) error {
	var one int
	err := s.db.QueryRow(
		`SELECT 1 FROM agents WHERE id = ? AND user_id = ?`, agentID, userID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
