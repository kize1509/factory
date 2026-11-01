// Package skills keeps local copies of a few skills repos, lists what's in
// them, and hands each role its chosen skills in the form its runtime
// understands.
package skills

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Source is one skills repo the factory keeps a copy of.
type Source struct {
	Key  string `json:"key"`  // folder under ~/.factory/skills; also the category when the repo has none
	Repo string `json:"repo"` // git URL
	Glob string `json:"glob"` // where its SKILL.md files sit inside the copy
	// Categorized means the folder above each skill names its category
	// (skills/<category>/<name>/SKILL.md).
	Categorized bool   `json:"-"`
	About       string `json:"about"`
}

// Sources lists the repos in the order the admin panel shows them. Skill
// names must be unique across all of them; when two clash, the first wins.
var Sources = []Source{
	{Key: "mattpocock", Repo: "https://github.com/mattpocock/skills", Glob: "skills/*/*/SKILL.md", Categorized: true,
		About: "engineering and writing skills"},
	{Key: "impeccable", Repo: "https://github.com/pbakaus/impeccable", Glob: "plugin/skills/*/SKILL.md",
		About: "visual design craft for frontend work: layout, type, color, motion, polish, audits"},
	{Key: "intent", Repo: "https://github.com/ghaida/intent", Glob: "skills/*/SKILL.md",
		About: "UX strategy: flows, states and edge cases, accessibility, UI copy, information architecture"},
}

type Skill struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Category    string `json:"category"`
	Description string `json:"description"`
	UserOnly    bool   `json:"user_only"` // a workflow a person starts; agents won't pick it up alone
	Dir         string `json:"dir"`
	Note        string `json:"note,omitempty"`
}

func home() string { h, _ := os.UserHomeDir(); return filepath.Join(h, ".factory") }

// Root is the folder that holds every local copy.
func Root() string { return filepath.Join(home(), "skills") }

// Dir is where a source's local copy lives.
func (src Source) Dir() string { return filepath.Join(Root(), src.Key) }

// Sync downloads each repo the first time and updates it after that. A
// repo that fails doesn't stop the others; the errors come back together.
func Sync() (map[string]string, error) {
	var errs []error
	for _, src := range Sources {
		if err := src.sync(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Key, err))
		}
	}
	return Versions(), errors.Join(errs...)
}

func (src Source) sync() error {
	var cmd *exec.Cmd
	if _, err := os.Stat(filepath.Join(src.Dir(), ".git")); err == nil {
		cmd = exec.Command("git", "-C", src.Dir(), "pull", "--ff-only", "-q")
	} else {
		os.MkdirAll(Root(), 0o755)
		cmd = exec.Command("git", "clone", "-q", "--depth", "1", src.Repo, src.Dir())
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Version is the local copy's commit and date, or "" when not downloaded.
func (src Source) Version() string {
	out, err := exec.Command("git", "-C", src.Dir(), "log", "-1", "--format=%h · %cs").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Versions maps each source's key to its Version.
func Versions() map[string]string {
	out := map[string]string{}
	for _, src := range Sources {
		out[src.Key] = src.Version()
	}
	return out
}

// notes warn about skills that clash with how the factory works.
var notes = map[string]string{
	"handoff":     "the factory has its own handoff note and tool; don't give this to agents",
	"tdd":         "writes tests, so only for tasks that are about tests (agents don't write tests by default)",
	"specify":     "packages a design handoff for people; the factory has its own handoff",
	"investigate": "plans research with real users; an agent can't run it",
	"present":     "interviews a person, then builds a slide deck",
}

// List reads every source's SKILL.md files.
func List() []Skill {
	var out []Skill
	seen := map[string]string{}
	for _, src := range Sources {
		files, _ := filepath.Glob(filepath.Join(src.Dir(), src.Glob))
		var mine []Skill
		for _, f := range files {
			dir := filepath.Dir(f)
			s := Skill{Name: filepath.Base(dir), Source: src.Key, Category: src.Key, Dir: dir}
			if src.Categorized {
				s.Category = filepath.Base(filepath.Dir(dir))
			}
			readFrontmatter(f, &s)
			if first, ok := seen[s.Name]; ok {
				s.Note = "same name as a skill in " + first + ", which wins"
			} else {
				seen[s.Name] = src.Key
				s.Note = notes[s.Name]
			}
			mine = append(mine, s)
		}
		sort.Slice(mine, func(a, b int) bool {
			if mine[a].Category != mine[b].Category {
				return mine[a].Category < mine[b].Category
			}
			return mine[a].Name < mine[b].Name
		})
		out = append(out, mine...)
	}
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
	var key string     // the key whose block value (> or |) is being read
	var block []string // that value's lines so far
	set := func(k, v string) {
		switch k {
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
	flush := func() {
		if key != "" {
			set(key, strings.Join(block, " "))
		}
		key, block = "", nil
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			if inside {
				flush()
				return
			}
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if key != "" && (line == "" || line[0] == ' ' || line[0] == '\t') {
			if t := strings.TrimSpace(line); t != "" {
				block = append(block, t)
			}
			continue
		}
		flush()
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if v != "" && strings.ContainsRune(">|", rune(v[0])) && len(strings.Trim(v[1:], "+-0123456789")) == 0 {
			key = k // a block value; its lines follow, indented
			continue
		}
		set(k, strings.Trim(v, `"'`))
	}
}

// Dirs finds the folders of the named skills; unknown names are skipped.
func Dirs(names []string) []string {
	by := map[string]string{}
	for _, s := range List() {
		if _, ok := by[s.Name]; !ok {
			by[s.Name] = s.Dir
		}
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
