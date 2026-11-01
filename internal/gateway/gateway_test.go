package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"factory/internal/events"
)

func TestMetersStreamedAnthropicUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pi/anthropic/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-5-5\",\"usage\":{\"input_tokens\":1000,\"cache_read_input_tokens\":2000,\"output_tokens\":1}}}\n\n")
		io.WriteString(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":500}}\n\n")
	}))
	defer upstream.Close()
	bus := events.NewBus(t.TempDir())
	ch, stop := bus.Subscribe("J1")
	defer stop()
	g := New(upstream.URL, bus)
	srv := httptest.NewServer(g)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/llm/J1/archimedes/pi/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-5-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	select {
	case e := <-ch:
		if e.Type != "usage" || e.Data["input"] != 1000 || e.Data["output"] != 500 || e.Data["cache_read"] != 2000 {
			t.Fatalf("got %+v", e)
		}
		want := (1000*2 + 500*10 + 2000*0.1) / 1e6 // claude-sonnet fallback price
		if usd := e.Data["usd"].(float64); usd < want-1e-9 || usd > want+1e-9 {
			t.Fatalf("usd %v want %v", usd, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no usage event")
	}
}
