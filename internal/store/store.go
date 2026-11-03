// Package store is the engine's SQLite database (~/.factory/factory.db):
// jobs, every event, and model prices. Each job's events.jsonl in its repo
// stays as a readable copy; the database is what the engine queries.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"factory/internal/events"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
  id       TEXT PRIMARY KEY,
  issue    TEXT,
  title    TEXT,
  repo     TEXT,
  spec     TEXT NOT NULL,          -- the job's spec as JSON
  state    TEXT,
  branch   TEXT,
  commit_sha TEXT,
  pr_url   TEXT,                   -- the job's pull request, once opened
  error    TEXT,
  started  TEXT,
  updated  TEXT
);
CREATE TABLE IF NOT EXISTS events (
  seq   INTEGER PRIMARY KEY AUTOINCREMENT,
  job   TEXT,
  agent TEXT,
  type  TEXT NOT NULL,
  time  TEXT NOT NULL,
  data  TEXT                       -- JSON
);
CREATE INDEX IF NOT EXISTS events_job  ON events(job, seq);
CREATE INDEX IF NOT EXISTS events_type ON events(type);
CREATE TABLE IF NOT EXISTS projects (
  repo  TEXT PRIMARY KEY,          -- the repository's path
  name  TEXT NOT NULL              -- its readable name, given by the planner
);
CREATE TABLE IF NOT EXISTS runs (
  id         TEXT PRIMARY KEY,     -- the run folder's name under .factory/runs
  repo       TEXT,                 -- the project's repo path
  setup      TEXT,                 -- model, runtime, prompt version, strategy
  commit_sha TEXT,
  kind       TEXT,                 -- job, scenario, static, tests
  feature    TEXT,
  technique  TEXT,
  pass       INTEGER NOT NULL,
  wall_ms    REAL,
  cost_usd   REAL,
  dir        TEXT,                 -- the run folder
  created    TEXT
);
CREATE INDEX IF NOT EXISTS runs_cell ON runs(feature, technique, commit_sha);
CREATE TABLE IF NOT EXISTS passes (
  id      TEXT PRIMARY KEY,
  project TEXT,
  state   TEXT,                    -- running, done, stopped, budget
  summary TEXT,                    -- JSON
  started TEXT,
  ended   TEXT
);
CREATE TABLE IF NOT EXISTS ledger (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  pass       TEXT NOT NULL,
  repo       TEXT,                 -- the project's repo path: feature ids repeat across projects
  feature    TEXT NOT NULL,
  technique  TEXT NOT NULL,
  agent      TEXT,
  commit_sha TEXT,
  run        TEXT NOT NULL,        -- the run folder: where result.json and the evidence are
  verdict    TEXT NOT NULL,        -- ok, finding, blocked
  note       TEXT,
  created    TEXT
);
CREATE INDEX IF NOT EXISTS ledger_pass ON ledger(pass, id);
CREATE INDEX IF NOT EXISTS ledger_repo ON ledger(repo, feature, technique);
CREATE TABLE IF NOT EXISTS ops_notes (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  repo    TEXT,
  job     TEXT,                    -- the job or pass the note belongs to
  issue   TEXT,                    -- tickets it is about, comma separated
  agent   TEXT,
  kind    TEXT NOT NULL,           -- did, decision, finding, blocker, user_note, answer
  text    TEXT NOT NULL,
  seq     INTEGER,                 -- the event it came from, when there is one
  created TEXT
);
CREATE INDEX IF NOT EXISTS ops_notes_repo ON ops_notes(repo, id);
CREATE TABLE IF NOT EXISTS ops_drafts (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  repo    TEXT,
  job     TEXT,
  agent   TEXT,
  action  TEXT NOT NULL,           -- create, update, move, comment, link, attach
  args    TEXT NOT NULL,           -- JSON
  summary TEXT,                    -- the action in plain words
  state   TEXT NOT NULL,           -- pending, approved, rejected, failed
  result  TEXT,                    -- the issue ID, or the error
  created TEXT
);
CREATE TABLE IF NOT EXISTS ops_created (
  issue   TEXT PRIMARY KEY,        -- tickets Hermes created
  job     TEXT,
  created TEXT
);
CREATE TABLE IF NOT EXISTS prices (
  model       TEXT PRIMARY KEY,    -- provider/id, e.g. anthropic/claude-sonnet-5-5
  input       REAL NOT NULL,       -- US dollars per million tokens
  output      REAL NOT NULL,
  cache_read  REAL NOT NULL DEFAULT 0,
  cache_write REAL NOT NULL DEFAULT 0,
  source      TEXT,                -- models.dev, litellm or manual
  updated     TEXT
);
`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; SQLite serialises anyway
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	// columns added after the first release; an existing database gets them here
	for _, col := range []string{"pr_url TEXT", "island INTEGER"} {
		if _, err := db.Exec(`ALTER TABLE jobs ADD COLUMN ` + col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("add column %s: %w", col, err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// AddEvent stores one event, keeps the job row's state, branch, commit, PR
// and error in step with it, and returns the event's sequence number.
func (s *Store) AddEvent(e events.Event) int64 {
	data, _ := json.Marshal(e.Data)
	t := e.Time.Format(time.RFC3339Nano)
	res, err := s.db.Exec(`INSERT INTO events(job, agent, type, time, data) VALUES(?,?,?,?,?)`, e.Job, e.Agent, e.Type, t, string(data))
	var seq int64
	if err == nil {
		seq, _ = res.LastInsertId()
	}
	if e.Job == "" {
		return seq
	}
	str := func(k string) string { v, _ := e.Data[k].(string); return v }
	switch e.Type {
	case "job.state":
		s.db.Exec(`UPDATE jobs SET state=?, updated=? WHERE id=?`, str("state"), t, e.Job)
	case "workspace.ready":
		s.db.Exec(`UPDATE jobs SET branch=?, updated=? WHERE id=?`, str("branch"), t, e.Job)
	case "job.committed":
		s.db.Exec(`UPDATE jobs SET commit_sha=?, updated=? WHERE id=?`, str("sha"), t, e.Job)
	case "job.pr_opened":
		s.db.Exec(`UPDATE jobs SET pr_url=?, updated=? WHERE id=?`, str("url"), t, e.Job)
	case "job.failed", "job.stuck":
		s.db.Exec(`UPDATE jobs SET error=?, updated=? WHERE id=?`, str("error"), t, e.Job)
	case "job.resumed":
		s.db.Exec(`UPDATE jobs SET error='', updated=? WHERE id=?`, t, e.Job)
	case "job.named":
		s.db.Exec(`UPDATE jobs SET title=?, updated=? WHERE id=?`, str("title"), t, e.Job)
	}
	return seq
}

// RecomputeUsage works out the dollars of every stored usage event again
// with current prices, so past costs follow a price update.
func (s *Store) RecomputeUsage(usd func(model string, in, out, cacheRead, cacheWrite float64) float64) (int, error) {
	rows, err := s.db.Query(`SELECT seq, data FROM events WHERE type='usage'`)
	if err != nil {
		return 0, err
	}
	type row struct {
		seq  int64
		data map[string]any
	}
	var list []row
	for rows.Next() {
		var r row
		var d string
		rows.Scan(&r.seq, &d)
		json.Unmarshal([]byte(d), &r.data)
		list = append(list, r)
	}
	rows.Close()
	n := 0
	for _, r := range list {
		f := func(k string) float64 { v, _ := r.data[k].(float64); return v }
		m, _ := r.data["model"].(string)
		r.data["usd"] = usd(m, f("input"), f("output"), f("cache_read"), f("cache_write"))
		d, _ := json.Marshal(r.data)
		if _, err := s.db.Exec(`UPDATE events SET data=? WHERE seq=?`, string(d), r.seq); err == nil {
			n++
		}
	}
	return n, nil
}

// Events returns a job's events in order.
func (s *Store) Events(job string) ([]events.Event, error) {
	rows, err := s.db.Query(`SELECT seq, job, agent, type, time, data FROM events WHERE job=? ORDER BY seq`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		var e events.Event
		var t, data string
		var agent sql.NullString
		if err := rows.Scan(&e.Seq, &e.Job, &agent, &e.Type, &t, &data); err != nil {
			return nil, err
		}
		e.Agent = agent.String
		e.Time, _ = time.Parse(time.RFC3339Nano, t)
		json.Unmarshal([]byte(data), &e.Data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// HasEvents tells whether the database already holds a job's events.
func (s *Store) HasEvents(job string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE job=?`, job).Scan(&n)
	return n > 0
}

// JobRow is one job as stored.
type JobRow struct {
	ID, Issue, Title, Repo, State, Branch, Commit, PR, Error string
	Spec                                                     json.RawMessage
	Started                                                  time.Time
	Island                                                   int // its cell on the Aegean map; -1 until it has one
}

func (s *Store) SaveJob(id, issue, title, repo string, spec any, started time.Time) error {
	data, _ := json.Marshal(spec)
	_, err := s.db.Exec(`INSERT INTO jobs(id, issue, title, repo, spec, state, started, updated) VALUES(?,?,?,?,?,'starting',?,?)
		ON CONFLICT(id) DO UPDATE SET issue=excluded.issue, title=excluded.title, repo=excluded.repo, spec=excluded.spec`,
		id, issue, title, repo, string(data), started.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Jobs() ([]JobRow, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(issue,''), COALESCE(title,''), COALESCE(repo,''), spec, COALESCE(state,''),
		COALESCE(branch,''), COALESCE(commit_sha,''), COALESCE(pr_url,''), COALESCE(error,''), COALESCE(started,''), COALESCE(island,-1) FROM jobs ORDER BY started`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRow
	for rows.Next() {
		var j JobRow
		var spec, started string
		if err := rows.Scan(&j.ID, &j.Issue, &j.Title, &j.Repo, &spec, &j.State, &j.Branch, &j.Commit, &j.PR, &j.Error, &started, &j.Island); err != nil {
			return nil, err
		}
		j.Spec = json.RawMessage(spec)
		j.Started, _ = time.Parse(time.RFC3339Nano, started)
		out = append(out, j)
	}
	return out, rows.Err()
}

// SetIsland stores the Aegean cell a job's island stands on.
func (s *Store) SetIsland(id string, cell int) error {
	_, err := s.db.Exec(`UPDATE jobs SET island=? WHERE id=?`, cell, id)
	return err
}

// DeleteJob removes a job and everything stored for it: its events (which
// hold its messages and costs) and its row.
func (s *Store) DeleteJob(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM events WHERE job=?`, `DELETE FROM jobs WHERE id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Projects maps each repository to its readable name.
func (s *Store) Projects() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT repo, name FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var repo, name string
		if err := rows.Scan(&repo, &name); err != nil {
			return nil, err
		}
		out[repo] = name
	}
	return out, rows.Err()
}

// SetProject names a repository.
func (s *Store) SetProject(repo, name string) error {
	_, err := s.db.Exec(`INSERT INTO projects(repo, name) VALUES(?,?) ON CONFLICT(repo) DO UPDATE SET name=excluded.name`, repo, name)
	return err
}

// Price is US dollars per million tokens for one model.
type Price struct {
	Model      string  `json:"model"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Source     string  `json:"source"`
	Updated    string  `json:"updated"`
}

func (s *Store) SavePrices(list []Price, keepManual bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	for _, p := range list {
		q := `INSERT INTO prices(model, input, output, cache_read, cache_write, source, updated) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(model) DO UPDATE SET input=excluded.input, output=excluded.output, cache_read=excluded.cache_read,
			cache_write=excluded.cache_write, source=excluded.source, updated=excluded.updated`
		if keepManual {
			q += ` WHERE prices.source != 'manual'`
		}
		if _, err := tx.Exec(q, p.Model, p.Input, p.Output, p.CacheRead, p.CacheWrite, p.Source, now); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Prices() ([]Price, error) {
	rows, err := s.db.Query(`SELECT model, input, output, cache_read, cache_write, COALESCE(source,''), COALESCE(updated,'') FROM prices ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Price
	for rows.Next() {
		var p Price
		if err := rows.Scan(&p.Model, &p.Input, &p.Output, &p.CacheRead, &p.CacheWrite, &p.Source, &p.Updated); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Note is one piece of project knowledge the operations agent holds.
type Note struct {
	ID      int64  `json:"id"`
	Repo    string `json:"repo"`
	Job     string `json:"job"`
	Issue   string `json:"issue,omitempty"`
	Agent   string `json:"agent"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Seq     int64  `json:"seq,omitempty"`
	Created string `json:"created"`
}

// AddNote stores a note.
func (s *Store) AddNote(n Note) error {
	_, err := s.db.Exec(`INSERT INTO ops_notes(repo, job, issue, agent, kind, text, seq, created) VALUES(?,?,?,?,?,?,?,?)`,
		n.Repo, n.Job, n.Issue, n.Agent, n.Kind, n.Text, n.Seq, time.Now().Format(time.RFC3339Nano))
	return err
}

// Notes returns a repo's notes, newest first. A non-empty word list keeps
// the notes that contain any of the words (case-insensitive); job and issue
// narrow it further when set.
func (s *Store) Notes(repo, job, issue string, words []string, limit int) ([]Note, error) {
	q := `SELECT id, COALESCE(repo,''), COALESCE(job,''), COALESCE(issue,''), COALESCE(agent,''), kind, text, COALESCE(seq,0), COALESCE(created,'') FROM ops_notes WHERE 1=1`
	var args []any
	if repo != "" {
		q += ` AND repo=?`
		args = append(args, repo)
	}
	if job != "" {
		q += ` AND job=?`
		args = append(args, job)
	}
	if issue != "" {
		q += ` AND (',' || issue || ',') LIKE ?`
		args = append(args, "%,"+issue+",%")
	}
	if len(words) > 0 {
		q += ` AND (`
		for i, w := range words {
			if i > 0 {
				q += ` OR `
			}
			q += `LOWER(text) LIKE ? OR LOWER(issue) LIKE ?`
			args = append(args, "%"+strings.ToLower(w)+"%", "%"+strings.ToLower(w)+"%")
		}
		q += `)`
	}
	if limit <= 0 {
		limit = 20
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Repo, &n.Job, &n.Issue, &n.Agent, &n.Kind, &n.Text, &n.Seq, &n.Created); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Draft is a tracker write waiting for your approval (the Propose level).
type Draft struct {
	ID      int64           `json:"id"`
	Repo    string          `json:"repo"`
	Job     string          `json:"job"`
	Agent   string          `json:"agent"`
	Action  string          `json:"action"`
	Args    json.RawMessage `json:"args"`
	Summary string          `json:"summary"`
	State   string          `json:"state"`
	Result  string          `json:"result,omitempty"`
	Created string          `json:"created"`
}

// AddDraft stores a pending draft and returns its id.
func (s *Store) AddDraft(d Draft) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO ops_drafts(repo, job, agent, action, args, summary, state, created) VALUES(?,?,?,?,?,?,'pending',?)`,
		d.Repo, d.Job, d.Agent, d.Action, string(d.Args), d.Summary, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Drafts lists drafts, newest first; state "" lists all.
func (s *Store) Drafts(state string) ([]Draft, error) {
	q := `SELECT id, COALESCE(repo,''), COALESCE(job,''), COALESCE(agent,''), action, args, COALESCE(summary,''), state, COALESCE(result,''), COALESCE(created,'') FROM ops_drafts`
	var args []any
	if state != "" {
		q += ` WHERE state=?`
		args = append(args, state)
	}
	rows, err := s.db.Query(q+` ORDER BY id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Draft
	for rows.Next() {
		var d Draft
		var a string
		if err := rows.Scan(&d.ID, &d.Repo, &d.Job, &d.Agent, &d.Action, &a, &d.Summary, &d.State, &d.Result, &d.Created); err != nil {
			return nil, err
		}
		d.Args = json.RawMessage(a)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Draft returns one draft.
func (s *Store) Draft(id int64) (Draft, error) {
	all, err := s.Drafts("")
	if err != nil {
		return Draft{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return Draft{}, fmt.Errorf("no draft %d", id)
}

// ClaimDraft moves a pending draft to the given state and reports whether
// it did: two clicks on Approve claim it once, so it runs once.
func (s *Store) ClaimDraft(id int64, state string) bool {
	res, err := s.db.Exec(`UPDATE ops_drafts SET state=? WHERE id=? AND state='pending'`, state, id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// FinishDraft records how a claimed draft ended.
func (s *Store) FinishDraft(id int64, state, result string) {
	s.db.Exec(`UPDATE ops_drafts SET state=?, result=? WHERE id=?`, state, result, id)
}

// MarkCreated remembers a ticket the factory created.
func (s *Store) MarkCreated(issue, job string) {
	s.db.Exec(`INSERT OR IGNORE INTO ops_created(issue, job, created) VALUES(?,?,?)`, issue, job, time.Now().Format(time.RFC3339Nano))
}

// Created tells whether the factory created a ticket.
func (s *Store) Created(issue string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM ops_created WHERE issue=?`, issue).Scan(&n)
	return n > 0
}
