package events

import "testing"

func TestEmitWritesLogAndNotifies(t *testing.T) {
	b := NewBus(t.TempDir())
	defer b.Close()
	ch, stop := b.Subscribe("J1")
	defer stop()
	b.Emit("J2", "", "ignored", nil)
	b.Emit("J1", "archimedes", "agent.tool", map[string]any{"tool": "bash"})

	got := <-ch
	if got.Type != "agent.tool" || got.Agent != "archimedes" {
		t.Fatalf("got %+v", got)
	}
	log, err := b.Replay("J1")
	if err != nil || len(log) != 1 || log[0].Data["tool"] != "bash" {
		t.Fatalf("replay %v %+v", err, log)
	}
}
