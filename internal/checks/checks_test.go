package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	"factory/internal/events"
)

func TestRunReportsFailures(t *testing.T) {
	bus := events.NewBus(t.TempDir())
	defer bus.Close()
	res := Run(context.Background(), bus, "J1", "oracle", t.TempDir(), []string{"echo ok", "echo broken; exit 3"}, 10*time.Second)
	if !res[0].Passed || res[1].Passed || res[1].ExitCode != 3 {
		t.Fatalf("%+v", res)
	}
	if AllPassed(res) {
		t.Fatal("AllPassed should be false")
	}
	if f := Failures(res); !strings.Contains(f, "echo broken; exit 3") || !strings.Contains(f, "broken") {
		t.Fatalf("failures text: %s", f)
	}
}
