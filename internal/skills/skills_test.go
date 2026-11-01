package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFrontmatter(t *testing.T) {
	cases := []struct {
		name, src, wantName, wantDesc string
		userOnly                      bool
	}{
		{"plain", "---\nname: pr\ndescription: \"Open a pull request.\"\ndisable-model-invocation: true\n---\nbody\n", "pr", "Open a pull request.", true},
		{"folded", "---\nname: journey\ndescription: >\n  Design flows end to end.\n  Trigger on navigation.\n\nuser-invocable: true\n---\n", "journey", "Design flows end to end. Trigger on navigation.", false},
		{"literal strip", "---\ndescription: |-\n  One.\n  Two.\nname: x\n---\n", "x", "One. Two.", false},
		{"block at end", "---\nname: y\ndescription: >\n  Last key.\n---\n", "y", "Last key.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "SKILL.md")
			os.WriteFile(p, []byte(c.src), 0o644)
			var s Skill
			readFrontmatter(p, &s)
			if s.Name != c.wantName || s.Description != c.wantDesc || s.UserOnly != c.userOnly {
				t.Fatalf("got %+v", s)
			}
		})
	}
}
