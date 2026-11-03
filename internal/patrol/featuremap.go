package patrol

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MapFeatures is the deterministic floor of the surveyor: one feature per
// folder that holds source files (Go packages, JS packages), from the repo's
// tree. IDs are stable (the folder path; "root" for the top). A surveyor
// agent can later add routes, screens and commands on top.
func MapFeatures(repo string) []Feature {
	seen := map[string]bool{}
	filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != repo && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".mjs") || strings.HasSuffix(name, ".js") {
			rel, _ := filepath.Rel(repo, filepath.Dir(path))
			seen[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	var out []Feature
	for rel := range seen {
		id := rel
		if rel == "." {
			id = "root"
		}
		out = append(out, Feature{ID: id, Paths: rel})
	}
	if len(out) == 0 {
		out = []Feature{{ID: "root", Paths: "."}}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func gitHead(repo string) (string, error) {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
