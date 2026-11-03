package store

import (
	"encoding/json"
	"time"
)

// Run is one harness run folder, indexed. The files stay the source of truth;
// this row is what queries (the ledger, trends, baselines) read.
type Run struct {
	ID        string  `json:"id"`
	Repo      string  `json:"repo,omitempty"`
	Setup     string  `json:"setup,omitempty"`
	Commit    string  `json:"commit"`
	Kind      string  `json:"kind"`
	Feature   string  `json:"feature,omitempty"`
	Technique string  `json:"technique,omitempty"`
	Pass      bool    `json:"pass"`
	WallMS    float64 `json:"wall_ms"`
	CostUSD   float64 `json:"cost_usd"`
	Dir       string  `json:"dir"`
}

// LedgerRow is one check in a pass: feature × technique × commit, pointing
// at the run folder that holds the evidence. Result fields come from runs.
type LedgerRow struct {
	ID        int64   `json:"id"`
	Pass      string  `json:"pass"`
	Repo      string  `json:"repo,omitempty"`
	Feature   string  `json:"feature"`
	Technique string  `json:"technique"`
	Agent     string  `json:"agent"`
	Commit    string  `json:"commit"`
	Run       string  `json:"run"`
	RunDir    string  `json:"run_dir"`
	Verdict   string  `json:"verdict"` // ok, finding, blocked
	Note      string  `json:"note"`
	WallMS    float64 `json:"wall_ms"`
	CostUSD   float64 `json:"cost_usd"`
	Time      string  `json:"time"`
}

// PassRow is a pass as stored.
type PassRow struct {
	ID      string          `json:"id"`
	Project string          `json:"project"`
	State   string          `json:"state"`
	Summary json.RawMessage `json:"summary,omitempty"`
	Started string          `json:"started"`
	Ended   string          `json:"ended,omitempty"`
}

func (s *Store) AddRun(r Run) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO runs(id, repo, setup, commit_sha, kind, feature, technique, pass, wall_ms, cost_usd, dir, created) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Repo, r.Setup, r.Commit, r.Kind, r.Feature, r.Technique, r.Pass, r.WallMS, r.CostUSD, r.Dir, time.Now().Format(time.RFC3339Nano))
	return err
}

func (s *Store) AddLedger(l LedgerRow) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO ledger(pass, repo, feature, technique, agent, commit_sha, run, verdict, note, created) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		l.Pass, l.Repo, l.Feature, l.Technique, l.Agent, l.Commit, l.Run, l.Verdict, l.Note, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Ledger lists a pass's rows in the order they were recorded, with the
// run's folder and metrics joined in.
func (s *Store) Ledger(pass string) ([]LedgerRow, error) {
	rows, err := s.db.Query(`SELECT l.id, l.pass, COALESCE(l.repo,''), l.feature, l.technique, l.agent, l.commit_sha, l.run, COALESCE(r.dir,''), l.verdict, l.note,
		COALESCE(r.wall_ms,0), COALESCE(r.cost_usd,0), l.created
		FROM ledger l LEFT JOIN runs r ON r.id = l.run WHERE l.pass=? ORDER BY l.id`, pass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LedgerRow{}
	for rows.Next() {
		var l LedgerRow
		var agent, commit, note *string
		if err := rows.Scan(&l.ID, &l.Pass, &l.Repo, &l.Feature, &l.Technique, &agent, &commit, &l.Run, &l.RunDir, &l.Verdict, &note, &l.WallMS, &l.CostUSD, &l.Time); err != nil {
			return nil, err
		}
		if agent != nil {
			l.Agent = *agent
		}
		if commit != nil {
			l.Commit = *commit
		}
		if note != nil {
			l.Note = *note
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LastChecked is when each feature × technique of one repo was last checked,
// for the scheduler: least recently checked goes first. Only that repo's
// rows count: feature ids like "root" repeat across projects. Key is feature + "\x00" + technique.
func (s *Store) LastChecked(repo string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT feature, technique, MAX(created) FROM ledger WHERE repo=? GROUP BY feature, technique`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var f, t, c string
		if err := rows.Scan(&f, &t, &c); err != nil {
			return nil, err
		}
		m[f+"\x00"+t] = c
	}
	return m, rows.Err()
}

func (s *Store) SavePass(p PassRow) error {
	_, err := s.db.Exec(`INSERT INTO passes(id, project, state, summary, started, ended) VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET state=excluded.state, summary=excluded.summary, ended=excluded.ended`,
		p.ID, p.Project, p.State, string(p.Summary), p.Started, p.Ended)
	return err
}

func (s *Store) Pass(id string) (*PassRow, error) {
	var p PassRow
	var sum, ended *string
	err := s.db.QueryRow(`SELECT id, project, state, summary, started, ended FROM passes WHERE id=?`, id).Scan(&p.ID, &p.Project, &p.State, &sum, &p.Started, &ended)
	if err != nil {
		return nil, err
	}
	if sum != nil && *sum != "" {
		p.Summary = json.RawMessage(*sum)
	}
	if ended != nil {
		p.Ended = *ended
	}
	return &p, nil
}
