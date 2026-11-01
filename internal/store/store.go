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
	for _, col := range []string{"pr_url TEXT"} {
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
		COALESCE(branch,''), COALESCE(commit_sha,''), COALESCE(pr_url,''), COALESCE(error,''), COALESCE(started,'') FROM jobs ORDER BY started`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRow
	for rows.Next() {
		var j JobRow
		var spec, started string
		if err := rows.Scan(&j.ID, &j.Issue, &j.Title, &j.Repo, &spec, &j.State, &j.Branch, &j.Commit, &j.PR, &j.Error, &started); err != nil {
			return nil, err
		}
		j.Spec = json.RawMessage(spec)
		j.Started, _ = time.Parse(time.RFC3339Nano, started)
		out = append(out, j)
	}
	return out, rows.Err()
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
