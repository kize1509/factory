package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// wireBase matches the JetBrains Central wire prefix in an agent's config:
// http://127.0.0.1:19516/wire/<id>
var wireBase = regexp.MustCompile(`(http://127\.0\.0\.1:\d+/wire/[0-9a-f]+)/`)

// FindWire returns the wire prefix. An explicit value wins; otherwise it is
// read from Claude Code's settings, then from Codex's config.
func FindWire(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	home, _ := os.UserHomeDir()
	if data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); err == nil {
		var s struct {
			Env map[string]string `json:"env"`
		}
		if json.Unmarshal(data, &s) == nil {
			if m := wireBase.FindStringSubmatch(s.Env["ANTHROPIC_BASE_URL"] + "/"); m != nil {
				return m[1], nil
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); err == nil {
		if m := wireBase.FindSubmatch(data); m != nil {
			return string(m[1]), nil
		}
	}
	return "", errors.New("no Central wire found: set it in ~/.factory/gateway.json or run `central add claude-code`")
}
