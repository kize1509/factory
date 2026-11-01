package config

import "testing"

func TestShipMode(t *testing.T) {
	for _, c := range []struct{ ticket, project, want string }{
		{"", "", "commit"}, {"", "commit", "commit"}, {"Push", "commit", "push"}, {"PR", "", "pr"}, {"deploy", "pr", ""}, {"", "yolo", ""},
	} {
		if got := ShipMode(c.ticket, c.project); got != c.want {
			t.Errorf("ShipMode(%q, %q) = %q, want %q", c.ticket, c.project, got, c.want)
		}
	}
}
