package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// triageCmd asks a running engine to start a triage run: the product agent
// goes over the repo's board.
//
//	factory triage [-project HF] [-level fix] [-jobs 2] [-minutes 30] [-focus "..."] [-engine URL] [repo]
func triageCmd(args []string) int {
	fs := flag.NewFlagSet("triage", flag.ExitOnError)
	project := fs.String("project", "", "the YouTrack project, when factory.yml has no tracker block")
	level := fs.String("level", "", "observe, propose, file or fix; default the tracker's level")
	jobs := fs.Int("jobs", 2, "jobs it may start; 0 only recommends")
	minutes := fs.Float64("minutes", 30, "how long the run may take")
	focus := fs.String("focus", "", "what to look at first, in your words")
	engine := fs.String("engine", "http://localhost:7777", "the engine's address")
	fs.Parse(args)
	repo := "."
	if fs.NArg() > 0 {
		repo = fs.Arg(0)
	}
	repo, _ = filepath.Abs(repo)
	body, _ := json.Marshal(map[string]any{"repo": repo, "project": *project, "level": *level, "max_jobs": *jobs, "minutes": *minutes, "focus": *focus})
	resp, err := http.Post(*engine+"/api/triage", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "is the engine running (factory serve)? %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "%s", out)
		return 1
	}
	var r struct {
		ID, Project, Level string
		MaxJobs            int `json:"max_jobs"`
	}
	json.Unmarshal(out, &r)
	fmt.Printf("triage run %s on %s (level %s, up to %d jobs)\nfollow it: %s/api/triage/%s\n", r.ID, r.Project, r.Level, r.MaxJobs, *engine, r.ID)
	return 0
}
