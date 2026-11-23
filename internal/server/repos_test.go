package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/job"
	"factory/internal/store"
)

func TestRepoChoice(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bus := events.NewBus(t.TempDir())
	mk := func() string {
		d, _ := filepath.EvalSymlinks(t.TempDir())
		exec.Command("git", "-C", d, "init", "-q").Run()
		return d
	}
	eliot, scraper, fresh := mk(), mk(), mk()
	s := &Server{Store: db, Bus: bus, jobs: map[string]*job.Job{
		"ELL-1": job.New(job.Spec{ID: "ELL-1", Issue: "ELL-1", Repo: eliot}, bus, nil),
		"MR-3":  job.New(job.Spec{ID: "MR-3", Issue: "MR-3", Repo: scraper}, bus, nil),
	}}
	none := config.Project{}
	cases := []struct {
		issue, repo string
		want        string
		hard        bool
	}{
		{"MR-4", scraper, "", false},                                          // ran here before
		{"MR-4", eliot, "MR tickets ran in " + filepath.Base(scraper), false}, // ran elsewhere
		{"NEW-1", fresh, "", false},                                           // a new project in a new repo: nothing to compare
		{"ELL-9", fresh, "ELL tickets ran in " + filepath.Base(eliot), false},
		{"XY-1", eliot, "has run ELL tickets, never XY ones", false},
	}
	for _, c := range cases {
		msg, hard := s.repoMismatch(c.issue, c.repo, none)
		if (c.want == "") != (msg == "") || !strings.Contains(msg, c.want) || hard != c.hard {
			t.Errorf("%s in %s: %q hard=%v, want %q", c.issue, filepath.Base(c.repo), msg, hard, c.want)
		}
	}
	mapped := config.Project{Tracker: config.Tracker{Kind: "youtrack", Project: "ELL"}}
	if msg, hard := s.repoMismatch("MR-4", eliot, mapped); !hard || !strings.Contains(msg, "maps it to the ELL project") {
		t.Errorf("factory.yml mismatch: %q %v", msg, hard)
	}

	h := s.Handler()
	get := func(path string, v any) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		json.Unmarshal(w.Body.Bytes(), v)
	}
	var sug struct {
		Project string
		Repos   []struct{ Path string }
	}
	get("/api/repos/suggest?issue=mr-12", &sug)
	if sug.Project != "MR" || len(sug.Repos) != 1 || sug.Repos[0].Path != scraper {
		t.Fatalf("%+v", sug)
	}

	// a folder that isn't a repository yet: the form can make it one
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@t"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@t"}} {
		t.Setenv(kv[0], kv[1])
	}
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(plain, "a.txt"), []byte("x"), 0o644)
	var info struct {
		Exists, Repo bool
		Origin       string
	}
	get("/api/repos/info?path="+plain, &info)
	if !info.Exists || info.Repo {
		t.Fatalf("%+v", info)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/repos/init", strings.NewReader(`{"path":"`+plain+`"}`)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	get("/api/repos/info?path="+plain, &info)
	if !info.Repo || info.Origin != "" {
		t.Fatalf("%+v", info)
	}
	w = httptest.NewRecorder()
	home, _ := os.UserHomeDir()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/repos/init", strings.NewReader(`{"path":"`+home+`"}`)))
	if w.Code != 400 {
		t.Fatal("the home folder must never become a repository")
	}
}
