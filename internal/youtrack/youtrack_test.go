package youtrack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer perm:x" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/issues/ABC-1" {
			w.Write([]byte(`{"idReadable":"ABC-1","summary":"fix login","description":"times out","project":{"shortName":"ABC"},"customFields":[{"name":"Ship mode","value":{"name":"PR"}},{"name":"Assignee","value":null}]}`))
			return
		}
		if r.URL.Path == "/api/issues/ABC-3" {
			w.Write([]byte(`{"idReadable":"ABC-3","summary":"no fields","customFields":[{"name":"Ship mode","value":null}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := Client{URL: srv.URL, Token: "perm:x"}
	is, err := c.Issue(context.Background(), "ABC-1")
	if err != nil || is.Summary != "fix login" || is.Description != "times out" {
		t.Fatalf("%v %+v", err, is)
	}
	if is.ShipMode() != "pr" {
		t.Fatalf("mode=%q", is.ShipMode())
	}
	bare, err := c.Issue(context.Background(), "ABC-3")
	if err != nil || bare.ShipMode() != "" {
		t.Fatalf("a ticket without a ship mode: %v mode=%q", err, bare.ShipMode())
	}
	if _, err := c.Issue(context.Background(), "ABC-2"); err == nil {
		t.Fatal("want not found")
	}
	if _, err := (Client{URL: srv.URL, Token: "bad"}).Issue(context.Background(), "ABC-1"); err == nil {
		t.Fatal("want refused")
	}
}

func TestWritesAndReads(t *testing.T) {
	var cmds, comments []string
	var attached string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer perm:x" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/api/admin/projects":
			w.Write([]byte(`[{"id":"0-1","shortName":"HF"},{"id":"0-2","shortName":"HFX"}]`))
		case r.URL.Path == "/api/issues" && r.Method == "POST":
			if !strings.Contains(string(body), `"id":"0-1"`) {
				w.WriteHeader(400)
			}
			w.Write([]byte(`{"idReadable":"HF-7"}`))
		case r.URL.Path == "/api/issues":
			if r.URL.Query().Get("query") != "project: HF login" {
				w.WriteHeader(400)
			}
			w.Write([]byte(`[{"idReadable":"HF-1","summary":"login","customFields":[{"name":"State","value":{"name":"Open"}}]}]`))
		case r.URL.Path == "/api/commands":
			cmds = append(cmds, string(body))
			w.Write([]byte(`{}`))
		case r.URL.Path == "/api/issues/HF-1/comments" && r.Method == "POST":
			comments = append(comments, string(body))
			w.Write([]byte(`{}`))
		case r.URL.Path == "/api/issues/HF-1/comments":
			w.Write([]byte(`[{"id":"1","text":"old","created":10,"author":{"login":"a"}},{"id":"2","text":"new","created":20,"author":{"login":"b"}}]`))
		case r.URL.Path == "/api/issues/HF-1/attachments":
			attached = r.Header.Get("Content-Type")
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/issues/HF-1/activities":
			w.Write([]byte(`[{"timestamp":1700000000000,"author":{"login":"a"},"field":{"name":"State"}}]`))
		case r.URL.Path == "/api/issues/HF-1":
			w.Write([]byte(`{"idReadable":"HF-1","summary":"login","description":"d","tags":[{"name":"factory-patrol"}],"customFields":[{"name":"State","value":{"name":"Open"}}],` +
				`"attachments":[{"name":"a.png"}],"comments":[{"id":"1","text":"hi","created":1,"author":{"login":"a"}}],"links":[{"direction":"BOTH","linkType":{"name":"Relates"},"issues":[{"idReadable":"HF-2"}]},{"direction":"OUTWARD","linkType":{"name":"Subtask"},"issues":[]}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, ctx := Client{URL: srv.URL, Token: "perm:x"}, context.Background()

	if id, err := c.Create(ctx, NewIssue{Project: "HF", Summary: "s", Description: "d"}); err != nil || id != "HF-7" {
		t.Fatalf("%q %v", id, err)
	}
	if _, err := c.Create(ctx, NewIssue{Project: "NOPE", Summary: "s"}); err == nil {
		t.Fatal("unknown project")
	}
	if err := c.Command(ctx, []string{"HF-7"}, "tag x"); err != nil || len(cmds) != 1 || !strings.Contains(cmds[0], `"query":"tag x"`) {
		t.Fatalf("%v %v", err, cmds)
	}
	if err := c.AddComment(ctx, "HF-1", "hello"); err != nil || len(comments) != 1 {
		t.Fatal(err)
	}
	list, err := c.Search(ctx, "project: HF login", 5)
	if err != nil || len(list) != 1 || list[0].State != "Open" {
		t.Fatalf("%v %+v", err, list)
	}
	news, err := c.Comments(ctx, "HF-1", 10)
	if err != nil || len(news) != 1 || news[0].Text != "new" {
		t.Fatalf("%v %+v", err, news)
	}
	d, err := c.Read(ctx, "HF-1")
	if err != nil || d.Tags[0] != "factory-patrol" || d.Fields["State"] != "Open" || len(d.Comments) != 1 || len(d.Links) != 1 || d.Links[0].Issues[0] != "HF-2" ||
		d.Attachments[0] != "a.png" || len(d.Activity) != 1 {
		t.Fatalf("%v %+v", err, d)
	}
	f := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(f, []byte("png"), 0o644)
	if err := c.Attach(ctx, "HF-1", f); err != nil || !strings.HasPrefix(attached, "multipart/form-data") {
		t.Fatalf("%v %q", err, attached)
	}
	if _, err := (Client{URL: srv.URL, Token: "bad"}).Search(ctx, "x", 1); err == nil {
		t.Fatal("want refused")
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{"Done": "Done", "In Progress": "{In Progress}", "a,b": "{a,b}"} {
		if got, err := Quote(in); err != nil || got != want {
			t.Errorf("Quote(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a}b", "x\ny", "{x"} {
		if _, err := Quote(bad); err == nil {
			t.Errorf("Quote(%q) should fail", bad)
		}
	}
}
