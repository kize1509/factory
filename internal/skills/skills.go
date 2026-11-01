// Package skills keeps a local copy of a skills repo (mattpocock/skills by
// default), lists what's in it, and hands each role its chosen skills in
// the form its runtime understands.
package skills

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const Repo = "https://github.com/mattpocock/skills"

type Skill struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	UserOnly    bool   `json:"user_only"` // a workflow a person starts; agents won't pick it up alone
	Dir         string `json:"dir"`
	Note        string `json:"note,omitempty"`
}

func home() string { h, _ := os.UserHomeDir(); return filepath.Join(h, ".factory") }

// Dir is where the local copy lives.
func Dir() string { return filepath.Join(home(), "skills", "mattpocock") }

// Sync downloads the repo the first time and updates it after that.
func Sync() (string, error) {
	var cmd *exec.Cmd
	if _, err := os.Stat(filepath.Join(Dir(), ".git")); err == nil {
		cmd = exec.Command("git", "-C", Dir(), "pull", "--ff-only", "-q")
	} else {
		os.MkdirAll(filepath.Dir(Dir()), 0o755)
		cmd = exec.Command("git", "clone", "-q", "--depth", "1", Repo, Dir())
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return Version(), nil
}

// Version is the local copy's commit and date, or "" when not downloaded.
func Version() string {
	out, err := exec.Command("git", "-C", Dir(), "log", "-1", "--format=%h · %cs").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// List reads every skills/<category>/<name>/SKILL.md.
func List() []Skill {
	files, _ := filepath.Glob(filepath.Join(Dir(), "skills", "*", "*", "SKILL.md"))
	var out []Skill
	for _, f := range files {
		dir := filepath.Dir(f)
		s := Skill{Name: filepath.Base(dir), Category: filepath.Base(filepath.Dir(dir)), Dir: dir}
		readFrontmatter(f, &s)
		switch s.Name {
		case "handoff":
			s.Note = "the factory has its own handoff note and tool; don't give this to agents"
		case "tdd":
			s.Note = "writes tests, so only for tasks that are about tests (agents don't write tests by default)"
		}
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Category != out[b].Category {
			return out[a].Category < out[b].Category
		}
		return out[a].Name < out[b].Name
	})
	return out
}

func readFrontmatter(path string, s *Skill) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inside := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			if inside {
				return
			}
			inside = true
			continue
		}
		if !inside {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			if v != "" {
				s.Name = v
			}
		case "description":
			s.Description = v
		case "disable-model-invocation":
			s.UserOnly = v == "true"
		}
	}
}

// Dirs finds the folders of the named skills; unknown names are skipped.
func Dirs(names []string) []string {
	by := map[string]string{}
	for _, s := range List() {
		by[s.Name] = s.Dir
	}
	var out []string
	for _, n := range names {
		if d, ok := by[n]; ok {
			out = append(out, d)
		}
	}
	return out
}

// PluginDir builds a small Claude Code plugin holding just these skills, so
// a Claude Code agent loads only what its role was given (--plugin-dir).
func PluginDir(role string, dirs []string) (string, error) {
	root := filepath.Join(home(), "skillsets", role)
	os.RemoveAll(root)
	if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
		return "", err
	}
	meta, _ := json.MarshalIndent(map[string]string{"name": "factory-" + role, "version": "1.0.0",
		"description": "Skills the factory gives its " + role + " agents"}, "", "  ")
	os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), meta, 0o644)
	os.MkdirAll(filepath.Join(root, "skills"), 0o755)
	for _, d := range dirs {
		if err := os.Symlink(d, filepath.Join(root, "skills", filepath.Base(d))); err != nil {
			return "", err
		}
	}
	return root, nil
}

// Instructions lists the skills in plain text, for runtimes that can't
// load them natively: the agent reads a SKILL.md when its task matches.
func Instructions(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	by := map[string]Skill{}
	for _, s := range List() {
		by[s.Dir] = s
	}
	var b strings.Builder
	b.WriteString("Skills you can use. When your work matches one, read its SKILL.md first and follow it:\n")
	for _, d := range dirs {
		s := by[d]
		fmt.Fprintf(&b, "- %s: %s (%s)\n", s.Name, s.Description, filepath.Join(d, "SKILL.md"))
	}
	return b.String()
}
