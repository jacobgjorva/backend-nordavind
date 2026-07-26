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
	ID              string `json:"id"`
	Name            string `json:"name"`
	Personality     string `json:"personality"`  // brukersatt lynne, farger tolknings-tonen og trollet i farmen
	Category        string `json:"category"`     // brukersatt kategori - styrer klynge og farge i agent-grafen
	HasResponse     bool   `json:"has_response"` // siste kjøring ga et resultat brukeren ikke har åpnet ennå
	Task            string `json:"task"`
	ConnectionID    string `json:"connection_id"`
	ScheduleLabel   string `json:"schedule_label"`   // menneskelig, f.eks. "hver dag kl 08:00"
	IntervalSeconds int    `json:"interval_seconds"` // hvor ofte den kjøres
	RunTime         string `json:"run_time"`         // HH:MM for dag/uke-intervaller
	DailyTokenLimit int    `json:"daily_token_limit"`
	WriteAccess     bool   `json:"write_access"`
	Enabled         bool   `json:"enabled"`
	PushEnabled     bool   `json:"push_enabled"`

	// Oppdrags-modus: agenten jobber mot et mål over flere kjøringer og gir seg
	// først når det er nådd.
	Mission          bool   `json:"mission"`
	SendMail         bool   `json:"send_mail"`         // tillatelse til å sende mail selv
	MissionState     string `json:"mission_state"`     // komprimert tilstand (JSON)
	MissionStatus    string `json:"mission_status"`    // running | done | paused
	MissionCriteria  string `json:"mission_criteria"`  // hva som teller som fullført mål
	CriteriaApproved bool   `json:"criteria_approved"` // bruker har godkjent kriteriene
	MissionBudget    int    `json:"mission_budget"`    // totalt token-tak for hele oppdraget (0 = ubegrenset)
	MissionActivity  string `json:"mission_activity"`  // live «hva jeg gjør/tenker» (JSON: thought + steps)

	// Kompilert plan: spinup finner én gang ut HVORDAN oppgaven løses best, og
	// lagrer stegene her. Hver kjøring utfører planen deterministisk i stedet
	// for å lete på nytt.
	Plan        string     `json:"plan"`        // JSON: steps + watch + alert_rule
	PlanStatus  string     `json:"plan_status"` // "" | building | ready | broken
	PlanError   string     `json:"plan_error"`  // hvorfor planen ikke ble bygget/er ødelagt
	PlanBuiltAt *time.Time `json:"plan_built_at,omitempty"`

	// Minne mellom kjøringer: rådataene forrige kjøring hentet. Er de identiske
	// denne gangen, har ingenting endret seg — da trengs verken tolkning eller
	// varsel. Snapshotet eksponeres ikke til klienten (kan være stort).
	LastSnapshot string `json:"-"`

	CreatedAt time.Time  `json:"created_at"`
	ChatID    string     `json:"chat_id"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`

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
		CREATE TABLE IF NOT EXISTS push_notifications (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id  TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			agent_id   TEXT NOT NULL,
			title      TEXT NOT NULL DEFAULT '',
			body       TEXT NOT NULL DEFAULT '',
			sent       INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_push_unsent ON push_notifications (sent, created_at);
	`); err != nil {
		return err
	}
	// Bakoverkompatibelt: legg til nye kolonner på eldre agents-tabeller.
	for _, col := range []string{
		`ALTER TABLE agents ADD COLUMN run_time TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN next_run_at TIMESTAMP`,
		`ALTER TABLE agents ADD COLUMN last_run_at TIMESTAMP`,
		`ALTER TABLE agents ADD COLUMN chat_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN push_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN mission INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN send_mail INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN mission_state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN mission_status TEXT NOT NULL DEFAULT 'running'`,
		`ALTER TABLE agents ADD COLUMN mission_criteria TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN criteria_approved INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN mission_budget INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN mission_activity TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN plan TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN plan_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN plan_error TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN plan_built_at TIMESTAMP`,
		`ALTER TABLE agents ADD COLUMN personality TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN category TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN response_seen_at TIMESTAMP`,
		`ALTER TABLE agent_runs ADD COLUMN alert INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN last_snapshot TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN last_snapshot_at TIMESTAMP`,
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
	id, err := newID()
	if err != nil {
		return a, err
	}
	a.ID = id
	a.CreatedAt = time.Now()
	a.Enabled = true
	next := NextRun(a.CreatedAt, a.IntervalSeconds, a.RunTime)
	a.NextRunAt = &next

	// Egen chat for agentens output, koblet begge veier.
	chatID, err := newID()
	if err != nil {
		return a, err
	}
	a.ChatID = chatID
	if _, err := s.db.Exec(
		`INSERT INTO chats (id, tenant_id, user_id, title, agent_id) VALUES (?, ?, ?, ?, ?)`,
		a.ChatID, tenantID, userID, a.Name, a.ID,
	); err != nil {
		return a, err
	}

	_, err = s.db.Exec(
		`INSERT INTO agents
			(id, tenant_id, user_id, name, task, connection_id, schedule_label,
			 interval_seconds, run_time, daily_token_limit, write_access, enabled, next_run_at, chat_id,
			 mission, send_mail, mission_status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, 'running')`,
		a.ID, tenantID, userID, a.Name, a.Task, a.ConnectionID, a.ScheduleLabel,
		a.IntervalSeconds, a.RunTime, a.DailyTokenLimit, boolToInt(a.WriteAccess), next, a.ChatID,
		boolToInt(a.Mission), boolToInt(a.SendMail),
	)
	return a, err
}

// ListAgents returnerer brukerens agenter, nyest først.
func (s *Store) ListAgents(userID string) ([]Agent, error) {
	rows, err := s.db.Query(
		`SELECT id, name, task, connection_id, schedule_label, interval_seconds,
		        run_time, daily_token_limit, write_access, enabled, created_at,
		        next_run_at, last_run_at, chat_id, mission, send_mail, mission_status, plan_status,
		        personality, mission_activity, category
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
		var write, enabled, mission, sendMail int
		if err := rows.Scan(&a.ID, &a.Name, &a.Task, &a.ConnectionID, &a.ScheduleLabel,
			&a.IntervalSeconds, &a.RunTime, &a.DailyTokenLimit, &write, &enabled,
			&a.CreatedAt, &a.NextRunAt, &a.LastRunAt, &a.ChatID,
			&mission, &sendMail, &a.MissionStatus, &a.PlanStatus,
			&a.Personality, &a.MissionActivity, &a.Category); err != nil {
			return nil, err
		}
		a.WriteAccess = write != 0
		a.Enabled = enabled != 0
		a.Mission = mission != 0
		a.SendMail = sendMail != 0
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Ulest svar: siste kjøring med faktisk output som er nyere enn sist
	// brukeren åpnet agent-chatten. Én spørring for alle agentene.
	unseen, err := s.db.Query(
		`SELECT r.agent_id
		 FROM agent_runs r
		 JOIN agents a ON a.id = r.agent_id
		 WHERE a.user_id = ? AND r.status = 'ok' AND r.output != ''
		 GROUP BY r.agent_id
		 HAVING MAX(r.started_at) > COALESCE(a.response_seen_at, '1970-01-01')`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer unseen.Close()
	fresh := map[string]bool{}
	for unseen.Next() {
		var id string
		if err := unseen.Scan(&id); err != nil {
			return nil, err
		}
		fresh[id] = true
	}
	for i := range out {
		out[i].HasResponse = fresh[out[i].ID]
	}
	return out, unseen.Err()
}

// AgentRunEvent er én kjøring i historikk-grafen: når, for hvilken agent,
// og om den la igjen et resultat.
type AgentRunEvent struct {
	AgentID   string    `json:"agent_id"`
	StartedAt time.Time `json:"started_at"`
	Status    string    `json:"status"`
	HasOutput bool      `json:"has_output"`
	Alert     bool      `json:"alert"`  // «Funn!» — kjøringen fant noe med verdi
	Output    string    `json:"output"` // meldingen (kappet), til pille-ekspansjonen
}

// AgentRunsSince returnerer alle kjøringer for brukerens agenter etter et
// tidspunkt (til trådgrafen), eldste først.
func (s *Store) AgentRunsSince(userID string, since time.Time) ([]AgentRunEvent, error) {
	rows, err := s.db.Query(
		`SELECT r.agent_id, r.started_at, r.status, r.output != '', r.alert,
		        substr(r.output, 1, 4000)
		 FROM agent_runs r
		 JOIN agents a ON a.id = r.agent_id
		 WHERE a.user_id = ? AND r.started_at >= ?
		 ORDER BY r.started_at`,
		userID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRunEvent
	for rows.Next() {
		var e AgentRunEvent
		var hasOut, alert int
		if err := rows.Scan(&e.AgentID, &e.StartedAt, &e.Status, &hasOut, &alert, &e.Output); err != nil {
			return nil, err
		}
		e.HasOutput = hasOut != 0
		e.Alert = alert != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkAgentResponseSeen registrerer at brukeren har åpnet agentens chat —
// noden glir da tilbake fra svar-seksjonen i grafen.
func (s *Store) MarkAgentResponseSeen(agentID, userID string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET response_seen_at = ? WHERE id = ? AND user_id = ?`,
		time.Now(), agentID, userID,
	)
	return err
}

// DueAgents returnerer aktiverte agenter som skal kjøres nå.
func (s *Store) DueAgents(now time.Time) ([]Agent, error) {
	rows, err := s.db.Query(
		`SELECT id, tenant_id, user_id, name, task, connection_id, schedule_label,
		        interval_seconds, run_time, daily_token_limit, write_access, next_run_at, chat_id, push_enabled,
		        mission, send_mail, mission_state, mission_status, plan, plan_status, last_snapshot, personality
		 FROM agents
		 WHERE enabled = 1 AND mission = 0 AND next_run_at IS NOT NULL AND next_run_at <= ?
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
		var write, push, mission, sendMail int
		if err := rows.Scan(&a.ID, &a.TenantID, &a.UserID, &a.Name, &a.Task,
			&a.ConnectionID, &a.ScheduleLabel, &a.IntervalSeconds, &a.RunTime,
			&a.DailyTokenLimit, &write, &a.NextRunAt, &a.ChatID, &push,
			&mission, &sendMail, &a.MissionState, &a.MissionStatus,
			&a.Plan, &a.PlanStatus, &a.LastSnapshot, &a.Personality); err != nil {
			return nil, err
		}
		a.WriteAccess = write != 0
		a.PushEnabled = push != 0
		a.Mission = mission != 0
		a.SendMail = sendMail != 0
		a.Enabled = true
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetPlanStatus setter plan-tilstanden (building/ready/broken) og en evt. årsak.
// Tømmer ikke selve planen — en «broken» plan beholdes til den er bygget på nytt.
// StuckPlanAgents er agenter der spinup ble avbrutt (typisk av en omstart
// midt i byggingen) — status «building» uten at noen jobber lenger.
func (s *Store) StuckPlanAgents() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM agents WHERE plan_status = 'building'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) SetPlanStatus(agentID, status, reason string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET plan_status = ?, plan_error = ? WHERE id = ?`,
		status, reason, agentID,
	)
	return err
}

// SetAgentPlan lagrer en ferdig kompilert plan og markerer den som klar.
func (s *Store) SetAgentPlan(agentID, plan string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET plan = ?, plan_status = 'ready', plan_error = '', plan_built_at = ?
		 WHERE id = ?`,
		plan, time.Now(), agentID,
	)
	return err
}

// ClearAgentPlan fjerner planen helt (brukes når oppgaven endres, så den gamle
// planen aldri kjøres mot en ny oppgave). Snapshotet nullstilles med, siden
// gamle data ikke er sammenlignbare med det en ny plan henter.
func (s *Store) ClearAgentPlan(agentID string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET plan = '', plan_status = '', plan_error = '', plan_built_at = NULL,
		        last_snapshot = '', last_snapshot_at = NULL
		 WHERE id = ?`, agentID,
	)
	return err
}

// AgentPersona er de brukersatte identitetsfeltene. Nil-peker = ikke endre;
// tom streng = fjern verdien.
type AgentPersona struct {
	Name        string
	Personality *string
	Category    *string
}

// SetAgentPersona setter navn, personlighet og/eller kategori.
// Chattens tittel følger navnet, som ellers.
func (s *Store) SetAgentPersona(agentID, userID string, p AgentPersona) error {
	if err := s.agentOwned(agentID, userID); err != nil {
		return err
	}
	if n := strings.TrimSpace(p.Name); n != "" {
		if _, err := s.db.Exec(`UPDATE agents SET name=? WHERE id=?`, n, agentID); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE chats SET title=? WHERE agent_id=?`, n, agentID); err != nil {
			return err
		}
	}
	if p.Personality != nil {
		if _, err := s.db.Exec(`UPDATE agents SET personality=? WHERE id=?`,
			strings.TrimSpace(*p.Personality), agentID); err != nil {
			return err
		}
	}
	if p.Category != nil {
		if _, err := s.db.Exec(`UPDATE agents SET category=? WHERE id=?`,
			strings.TrimSpace(*p.Category), agentID); err != nil {
			return err
		}
	}
	return nil
}

// SetAgentSnapshot lagrer rådataene fra siste kjøring, så neste kjøring kan se
// hva som faktisk er endret.
func (s *Store) SetAgentSnapshot(agentID, snapshot string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET last_snapshot = ?, last_snapshot_at = ? WHERE id = ?`,
		snapshot, time.Now(), agentID,
	)
	return err
}

// AgentPlanStatus leser plan-feltene for én agent.
func (s *Store) AgentPlanStatus(agentID string) (plan, status, planErr string, err error) {
	err = s.db.QueryRow(
		`SELECT plan, plan_status, plan_error FROM agents WHERE id = ?`, agentID,
	).Scan(&plan, &status, &planErr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	return plan, status, planErr, err
}

// SetMissionState lagrer agentens komprimerte oppdrags-tilstand mellom kjøringer.
func (s *Store) SetMissionState(agentID, state string) error {
	_, err := s.db.Exec(`UPDATE agents SET mission_state = ? WHERE id = ?`, state, agentID)
	return err
}

// FinishMission markerer oppdraget som fullført og stopper videre kjøring.
func (s *Store) FinishMission(agentID string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET mission_status = 'done', enabled = 0 WHERE id = ?`, agentID,
	)
	return err
}

// PauseMission stopper løkka uten å markere målet nådd (f.eks. token-tak truffet).
func (s *Store) PauseMission(agentID, reason string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET mission_status = ?, enabled = 0 WHERE id = ?`, reason, agentID,
	)
	return err
}

// SetMissionPlan lagrer mål (task), fullført-kriterier og token-tak. Kriteriene
// må godkjennes før løkka kan starte, så approved nullstilles.
func (s *Store) SetMissionPlan(id, userID, goal, criteria string, budget int) error {
	if err := s.agentOwned(id, userID); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`UPDATE agents SET task=?, mission=1, mission_criteria=?, mission_budget=?,
		        criteria_approved=0, mission_status='running', mission_state='', enabled=0
		 WHERE id=? AND user_id=?`,
		goal, criteria, budget, id, userID,
	)
	return err
}

// ApproveMission godkjenner kriteriene og aktiverer løkka.
func (s *Store) ApproveMission(id, userID string) error {
	res, err := s.db.Exec(
		`UPDATE agents SET criteria_approved=1, enabled=1, mission_status='running'
		 WHERE id=? AND user_id=? AND mission=1`,
		id, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MissionTokensSpent summerer alle tokens oppdraget har brukt (mot token-taket).
func (s *Store) MissionTokensSpent(agentID string) (int, error) {
	var n sql.NullInt64
	err := s.db.QueryRow(
		`SELECT SUM(tokens_used) FROM agent_runs WHERE agent_id = ?`, agentID,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n.Int64), nil
}

// missionScanCols er felt-lista for å lese en agent med alle oppdrags-felt.
const missionScanCols = `id, tenant_id, user_id, name, task, connection_id, chat_id, schedule_label,
	daily_token_limit, enabled, push_enabled,
	mission, send_mail, mission_state, mission_status, mission_criteria, criteria_approved, mission_budget,
	mission_activity`

func scanMission(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var enabled, push, mission, sendMail, approved int
	err := sc.Scan(&a.ID, &a.TenantID, &a.UserID, &a.Name, &a.Task, &a.ConnectionID, &a.ChatID,
		&a.ScheduleLabel, &a.DailyTokenLimit, &enabled, &push,
		&mission, &sendMail, &a.MissionState, &a.MissionStatus, &a.MissionCriteria, &approved, &a.MissionBudget,
		&a.MissionActivity)
	a.Enabled = enabled != 0
	a.PushEnabled = push != 0
	a.Mission = mission != 0
	a.SendMail = sendMail != 0
	a.CriteriaApproved = approved != 0
	return a, err
}

// SetMissionActivity lagrer den live «hva jeg gjør»-tilstanden (tom = tømt).
func (s *Store) SetMissionActivity(agentID, activity string) error {
	_, err := s.db.Exec(`UPDATE agents SET mission_activity = ? WHERE id = ?`, activity, agentID)
	return err
}

// MissionAgent leser én agent med alle oppdrags-felt (til løkke-runneren).
func (s *Store) MissionAgent(id string) (Agent, error) {
	a, err := scanMission(s.db.QueryRow(`SELECT `+missionScanCols+` FROM agents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// RunningMissions er godkjente, aktive oppdrag som ikke er ferdige — startes som
// kontinuerlige løkker (også etter en omstart av serveren).
func (s *Store) RunningMissions() ([]Agent, error) {
	rows, err := s.db.Query(
		`SELECT ` + missionScanCols + ` FROM agents
		 WHERE mission=1 AND enabled=1 AND criteria_approved=1 AND mission_status='running'`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanMission(rows)
		if err != nil {
			return nil, err
		}
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
	Alert      bool // kjøringen fant noe med verdi (agentens egen varselregel)
}

// RecordRun logger resultatet av en agent-kjøring.
func (s *Store) RecordRun(agentID string, r AgentRun) error {
	_, err := s.db.Exec(
		`INSERT INTO agent_runs (agent_id, status, output, tokens_used, error, alert)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		agentID, r.Status, r.Output, r.TokensUsed, r.Error, boolToInt(r.Alert),
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

// SetAgentSchedule setter frekvensen (og evt. klokkeslett) og planlegger
// neste kjøring på nytt. Brukes av Start-noden i flyt-visningen.
func (s *Store) SetAgentSchedule(id, userID string, intervalSeconds int, runTime, label string) error {
	if err := s.agentOwned(id, userID); err != nil {
		return err
	}
	if intervalSeconds < 900 {
		intervalSeconds = 900 // minimum 15 min
	}
	next := NextRun(time.Now(), intervalSeconds, runTime)
	_, err := s.db.Exec(
		`UPDATE agents SET interval_seconds=?, run_time=?, schedule_label=?, next_run_at=?
		 WHERE id=? AND user_id=?`,
		intervalSeconds, strings.TrimSpace(runTime), strings.TrimSpace(label), next, id, userID,
	)
	return err
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

// EnqueuePush legger et push-varsel i køen (sent=0). Selve leveringen til
// mobil-appen kobles på senere; nå lagres/logges bare varselet.
func (s *Store) EnqueuePush(tenantID, userID, agentID, title, body string) error {
	_, err := s.db.Exec(
		`INSERT INTO push_notifications (tenant_id, user_id, agent_id, title, body) VALUES (?, ?, ?, ?, ?)`,
		tenantID, userID, agentID, title, body,
	)
	return err
}

// SetAgentPush slår push-varsel på/av for en agent.
func (s *Store) SetAgentPush(agentID, userID string, on bool) error {
	res, err := s.db.Exec(
		`UPDATE agents SET push_enabled = ? WHERE id = ? AND user_id = ?`,
		boolToInt(on), agentID, userID,
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
	var write, enabled, mission, approved int
	err := s.db.QueryRow(
		`SELECT id, name, task, connection_id, schedule_label, interval_seconds,
		        run_time, daily_token_limit, write_access, enabled, created_at, chat_id,
		        mission, mission_status, mission_criteria, criteria_approved, mission_budget,
		        plan, plan_status, plan_error
		 FROM agents WHERE id = ? AND user_id = ?`,
		id, userID,
	).Scan(&a.ID, &a.Name, &a.Task, &a.ConnectionID, &a.ScheduleLabel,
		&a.IntervalSeconds, &a.RunTime, &a.DailyTokenLimit, &write, &enabled,
		&a.CreatedAt, &a.ChatID,
		&mission, &a.MissionStatus, &a.MissionCriteria, &approved, &a.MissionBudget,
		&a.Plan, &a.PlanStatus, &a.PlanError)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.WriteAccess = write != 0
	a.Enabled = enabled != 0
	a.Mission = mission != 0
	a.CriteriaApproved = approved != 0
	return a, err
}

// AgentByChat henter agenten som eier en chat (for pause-knappen i UI-et).
func (s *Store) AgentByChat(chatID, userID string) (Agent, error) {
	var a Agent
	var write, enabled, push, mission, sendMail, approved int
	err := s.db.QueryRow(
		`SELECT id, name, task, connection_id, schedule_label, interval_seconds,
		        run_time, daily_token_limit, write_access, enabled, created_at,
		        next_run_at, last_run_at, chat_id, push_enabled,
		        mission, send_mail, mission_status, mission_criteria, criteria_approved, mission_budget,
		        mission_activity, plan, plan_status, plan_error, plan_built_at
		 FROM agents WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	).Scan(&a.ID, &a.Name, &a.Task, &a.ConnectionID, &a.ScheduleLabel,
		&a.IntervalSeconds, &a.RunTime, &a.DailyTokenLimit, &write, &enabled,
		&a.CreatedAt, &a.NextRunAt, &a.LastRunAt, &a.ChatID, &push,
		&mission, &sendMail, &a.MissionStatus, &a.MissionCriteria, &approved, &a.MissionBudget,
		&a.MissionActivity, &a.Plan, &a.PlanStatus, &a.PlanError, &a.PlanBuiltAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.WriteAccess = write != 0
	a.Enabled = enabled != 0
	a.PushEnabled = push != 0
	a.Mission = mission != 0
	a.SendMail = sendMail != 0
	a.CriteriaApproved = approved != 0
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

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if chatID != "" {
		if _, err := tx.Exec(`DELETE FROM chat_messages WHERE chat_id = ?`, chatID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM chats WHERE id = ?`, chatID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM agent_runs WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM agents WHERE id = ? AND user_id = ?`, agentID, userID); err != nil {
		return err
	}
	return tx.Commit()
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
