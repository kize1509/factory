package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"factory/internal/config"
	"factory/internal/ops"
	"factory/internal/triage"
	"factory/internal/youtrack"
)

// The Board panel's data, the automatic schedule you can turn on per repo,
// and the runs kept from before a restart.

// loadTriage brings back the runs in ~/.factory/triage, so the panel and
// the schedule know what happened before the engine restarted.
func (s *Server) loadTriage() {
	dirs, _ := filepath.Glob(filepath.Join(config.Home(), "triage", "T*"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.triage == nil {
		s.triage = map[string]*triage.Run{}
	}
	for _, d := range dirs {
		if _, ok := s.triage[filepath.Base(d)]; ok {
			continue
		}
		if r, err := triage.Load(d); err == nil && r.ID != "" {
			s.triage[r.ID] = r
		}
	}
}

// runsFor lists repo's runs ("" for all), newest first.
func (s *Server) runsFor(repo string) []*triage.Run {
	s.mu.Lock()
	var out []*triage.Run
	for _, r := range s.triage {
		if repo == "" || r.Repo == repo {
			out = append(out, r)
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out
}

// Board is one repo as the Board panel shows it.
type Board struct {
	Repo     string            `json:"repo"`
	Name     string            `json:"name"`
	Project  string            `json:"project"`             // the YouTrack project, "" when unknown
	Mapped   bool              `json:"mapped"`              // factory.yml names the project
	Level    string            `json:"level"`               // the tracker's level (Hermes's); runs use fix
	Auto     config.TriageAuto `json:"auto"`                // when it runs on its own
	Running  string            `json:"running,omitempty"`   // the run going now
	LastAuto string            `json:"last_auto,omitempty"` // when the schedule last looked, and what it decided
	Live     []triage.LiveJob  `json:"live"`
}

// boards lists every repo the factory knows: the ones jobs ran in and the
// ones with a schedule.
func (s *Server) boards(w http.ResponseWriter, r *http.Request) {
	names, _ := s.Store.Projects()
	repos := map[string]bool{}
	for repo := range names {
		repos[repo] = true
	}
	guess := map[string]map[string]int{} // repo -> ticket project -> jobs
	s.mu.Lock()
	for _, j := range s.jobs {
		repos[j.Repo] = true
		if p, _, ok := strings.Cut(j.Issue, "-"); ok && p != "" {
			if guess[j.Repo] == nil {
				guess[j.Repo] = map[string]int{}
			}
			guess[j.Repo][strings.ToUpper(p)]++
		}
	}
	for _, t := range s.triage {
		repos[t.Repo] = true
	}
	s.mu.Unlock()
	settings := config.LoadEngine()
	for repo := range settings.Triage {
		repos[repo] = true
	}
	var out []Board
	for repo := range repos {
		if repo == "" {
			continue
		}
		b := Board{Repo: repo, Name: names[repo], Auto: settings.Triage[repo].Normalised(), Live: s.liveJobs(repo)}
		if b.Name == "" {
			b.Name = filepath.Base(repo)
		}
		if b.Live == nil {
			b.Live = []triage.LiveJob{}
		}
		if p, err := config.LoadProject(repo); err == nil {
			tr := p.Tracker.Normalised()
			b.Level = tr.Level
			if strings.EqualFold(tr.Kind, "youtrack") && tr.Project != "" {
				b.Project, b.Mapped = tr.Project, true
			}
		}
		if b.Project == "" {
			b.Project = b.Auto.Project
		}
		runs := s.runsFor(repo)
		if b.Project == "" && len(runs) > 0 { // the project its last run went over
			b.Project = runs[0].Project
		}
		if b.Project == "" { // the project most of its jobs' tickets are in
			n := 0
			for p, c := range guess[repo] {
				if c > n || c == n && p < b.Project {
					b.Project, n = p, c
				}
			}
		}
		for _, run := range runs {
			if run.StateNow() == triage.Running {
				b.Running = run.ID
			}
		}
		s.autoMu.Lock()
		b.LastAuto = s.autoNote[repo]
		s.autoMu.Unlock()
		out = append(out, b)
	}
	sort.Slice(out, func(a, b int) bool { return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name) })
	yt := settings.YouTrack
	writeJSON(w, map[string]any{"boards": out, "youtrack": map[string]any{"url": yt.URL, "configured": (youtrack.Client{URL: yt.URL, Token: yt.Token}).Configured()},
		"levels": config.Levels})
}

// putAuto saves when the product agent runs on its own for one repo.
func (s *Server) putAuto(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Repo string `json:"repo"`
		config.TriageAuto
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Repo == "" {
		http.Error(w, "pick a repository", http.StatusBadRequest)
		return
	}
	repo, err := repoRoot(b.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a := b.TriageAuto
	if a.Level != "" && config.ValidLevel(a.Level) == "" {
		http.Error(w, "level is observe, propose, file or fix", http.StatusBadRequest)
		return
	}
	if a.MaxJobs < 0 || a.MaxJobs > 10 {
		http.Error(w, "it may start 0 to 10 jobs", http.StatusBadRequest)
		return
	}
	a.Project = strings.ToUpper(strings.TrimSpace(a.Project))
	if a.On {
		if _, _, err := boardProject(repo, a.Project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// he always picks up an owner's answer; with nothing else picked he
		// also wakes for board changes and for finished jobs
		if !a.OnBoard && !a.OnReady {
			a.OnBoard, a.OnReady = true, true
		}
		a.OnAnswers = true
	}
	e := config.LoadEngine()
	if e.Triage == nil {
		e.Triage = map[string]config.TriageAuto{}
	}
	if !a.On && a == (config.TriageAuto{}) {
		delete(e.Triage, repo)
	} else {
		e.Triage[repo] = a
	}
	if err := config.SaveEngine(e); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.autoMu.Lock()
	delete(s.autoNote, repo)
	s.autoMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "auto": a.Normalised()})
}

// ---- the schedule ----

// triageLoop looks at every repo with a schedule every so often.
func (s *Server) triageLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.autoTick(time.Now())
		}
	}
}

// autoTick starts the automatic runs that are due.
func (s *Server) autoTick(now time.Time) {
	settings := config.LoadEngine()
	for repo, a := range settings.Triage {
		if !a.On {
			continue
		}
		note := s.autoCheck(repo, a, now, settings)
		s.autoMu.Lock()
		if s.autoNote == nil {
			s.autoNote = map[string]string{}
		}
		s.autoNote[repo] = now.Format("15:04") + ": " + note
		s.autoMu.Unlock()
	}
}

// autoCheck decides for one repo and starts the run when it's due. It
// returns what it decided, for the panel.
func (s *Server) autoCheck(repo string, a config.TriageAuto, now time.Time, settings config.Engine) string {
	a = a.Normalised()
	runs := s.runsFor(repo)
	var last *triage.Run
	today := 0
	y, m, d := now.Date()
	for _, r := range runs {
		if last == nil {
			last = r
		}
		if r.Auto {
			if ry, rm, rd := r.Since().Date(); ry == y && rm == m && rd == d {
				today++
			}
		}
	}
	if last != nil && last.StateNow() == triage.Running {
		return "a run is going"
	}
	if today >= a.PerDay {
		return fmt.Sprintf("waiting: %d automatic runs today, the most it may", today)
	}
	_, project, err := boardProject(repo, a.Project)
	if err != nil {
		return "can't run: " + err.Error()
	}
	var sig triage.Signals
	if last != nil {
		if now.Sub(last.EndedAt()) < time.Duration(a.Gap)*time.Minute {
			return fmt.Sprintf("waiting: less than %d minutes since the last run", a.Gap)
		}
		sig = s.signals(project, last, settings)
	}
	why := triage.Due(a, now, last, today, len(s.liveJobs(repo)), sig)
	if why == "" {
		return "nothing new on the board"
	}
	n := a.MaxJobs
	run, _, err := s.beginTriage(triageOpts{Repo: repo, Project: project, Level: a.Level, MaxJobs: &n, Minutes: a.Minutes, auto: true, why: why})
	if err != nil {
		return "couldn't start: " + err.Error()
	}
	return "started run " + run.ID + ": " + why
}

// signals reads what happened on the board since the last run: tickets a
// person changed (later than the factory last wrote to them) and answers to
// the last run's questions.
func (s *Server) signals(project string, last *triage.Run, settings config.Engine) triage.Signals {
	var sig triage.Signals
	c := youtrack.Client{URL: settings.YouTrack.URL, Token: settings.YouTrack.Token}
	if !c.Configured() {
		return sig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if changes, err := c.Changed(ctx, project, last.Since(), 50); err == nil {
		for _, ch := range changes {
			if s.Ops != nil && !ch.Updated.After(s.Ops.Touched(ch.ID).Add(5*time.Second)) {
				continue // the factory's own change
			}
			sig.Changed = append(sig.Changed, ch.ID)
		}
	}
	seen := map[string]bool{}
	for _, q := range last.Pending() {
		if seen[q.Issue] {
			continue
		}
		list, err := c.Comments(ctx, q.Issue, q.At.UnixMilli())
		if err != nil {
			continue
		}
		for _, cm := range list {
			if !strings.HasPrefix(cm.Text, ops.Marker) && cm.Created > q.At.UnixMilli() {
				sig.Answered, seen[q.Issue] = append(sig.Answered, q.Issue), true
				break
			}
		}
	}
	return sig
}
