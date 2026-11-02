package server

import (
	"encoding/json"
	"os"
	"path/filepath"

	"factory/internal/config"
	"factory/internal/job"
)

// Jobs live in SQLite (~/.factory/factory.db), so their logs, the map, the
// Chronicle and the metrics survive a restart.

// LoadJobs brings back earlier jobs from the database. A job that was
// still running when the engine stopped is marked interrupted; it and a
// stuck job can be guided on from what they wrote down. Jobs from
// the old jobs.json are imported once, with their events.jsonl.
func (s *Server) LoadJobs() {
	s.importJobsFile()
	rows, err := s.Store.Jobs()
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]*job.Job{}
	}
	for _, row := range rows {
		var spec job.Spec
		if json.Unmarshal(row.Spec, &spec) != nil {
			continue
		}
		j := job.New(spec, s.Bus, s.Runtimes[spec.Runtime])
		s.Bus.Place(j.ID, j.Dir())
		j.State, j.Branch, j.Commit, j.PR, j.Error, j.Started = row.State, row.Branch, row.Commit, row.PR, row.Error, row.Started
		if row.Title != "" {
			j.Title = row.Title // the planner's name for it
		}
		if j.State != "done" && j.State != "failed" && j.State != "stuck" && j.State != "cancelled" {
			j.State = "interrupted"
		}
		if j.State == "stuck" {
			j.Restore()
		}
		j.SetRuntimes(s.Runtimes)
		s.jobs[j.ID] = j
	}
}

func (s *Server) importJobsFile() {
	path := filepath.Join(config.Home(), "jobs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var specs []job.Spec
	if json.Unmarshal(data, &specs) == nil {
		for _, spec := range specs {
			j := job.New(spec, s.Bus, nil)
			s.Bus.Place(j.ID, j.Dir())
			log, _ := s.Bus.ReplayFile(j.ID)
			started := j.Started
			if len(log) > 0 {
				started = log[0].Time
			}
			s.Store.SaveJob(spec.ID, spec.Issue, spec.Title, spec.Repo, spec, started)
			if !s.Store.HasEvents(spec.ID) {
				for _, e := range log {
					s.Store.AddEvent(e)
				}
			}
		}
	}
	os.Rename(path, path+".imported")
}
