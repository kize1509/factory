// Package handoff defines the note every phase leaves for the next one
// (PRD §7.6) and checks that it is complete.
package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Note struct {
	From          string   `json:"from"`
	To            string   `json:"to"`
	Did           string   `json:"did"`
	Decisions     []string `json:"decisions"`
	Findings      []string `json:"findings"`
	Files         []string `json:"files"`
	OpenQuestions []string `json:"open_questions"`
	Next          string   `json:"next"`
	Evidence      []string `json:"evidence,omitempty"`
}

// Problems lists what is wrong with a note. Paths in files and evidence are
// checked against the worktree, then the job folder and its scratch folder,
// and may also be absolute.
func (n *Note) Problems(worktree, jobDir string) []string {
	var p []string
	need := map[string]string{"from": n.From, "to": n.To, "did": n.Did, "next": n.Next}
	for _, k := range []string{"from", "to", "did", "next"} {
		if strings.TrimSpace(need[k]) == "" {
			p = append(p, k+" is empty")
		}
	}
	for _, f := range append(append([]string{}, n.Files...), n.Evidence...) {
		if !exists(filepath.Join(worktree, f)) && !exists(filepath.Join(jobDir, f)) && !exists(filepath.Join(jobDir, "scratch", f)) && !(filepath.IsAbs(f) && exists(f)) {
			p = append(p, fmt.Sprintf("%s does not exist", f))
		}
	}
	return p
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }
