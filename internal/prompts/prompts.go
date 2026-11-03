// Package prompts holds the system prompt for each role. The defaults are
// the markdown files in roles/, built into the engine; a file with the same
// name in ~/.factory/prompts overrides one (edited in Admin → Prompts).
package prompts

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed roles/*.md
var defaults embed.FS

func overrideDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".factory", "prompts")
}

// For returns the role's prompt: your override if there is one, else the default.
func For(role string) string {
	if data, err := os.ReadFile(filepath.Join(overrideDir(), role+".md")); err == nil && strings.TrimSpace(string(data)) != "" {
		return string(data)
	}
	return Default(role)
}

// System is the system prompt an agent of a job starts with: its role's
// prompt, then the team rules (roles/team.md). The rules are appended here, so
// your own version of a role's prompt doesn't drop them.
func System(role string) string {
	return strings.TrimRight(For(role), "\n") + "\n\n" + strings.TrimRight(For("team"), "\n") + "\n"
}

func Default(role string) string {
	data, _ := defaults.ReadFile("roles/" + role + ".md")
	return string(data)
}

func Overridden(role string) bool {
	_, err := os.Stat(filepath.Join(overrideDir(), role+".md"))
	return err == nil
}

// Save stores your version of a role's prompt.
func Save(role, text string) error {
	if err := os.MkdirAll(overrideDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(overrideDir(), role+".md"), []byte(text), 0o600)
}

// Reset removes your version, going back to the default.
func Reset(role string) error {
	err := os.Remove(filepath.Join(overrideDir(), role+".md"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
