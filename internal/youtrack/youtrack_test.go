package youtrack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer perm:x" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/issues/ABC-1" {
			w.Write([]byte(`{"idReadable":"ABC-1","summary":"fix login","description":"times out","project":{"shortName":"ABC"}}`))
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
	if _, err := c.Issue(context.Background(), "ABC-2"); err == nil {
		t.Fatal("want not found")
	}
	if _, err := (Client{URL: srv.URL, Token: "bad"}).Issue(context.Background(), "ABC-1"); err == nil {
		t.Fatal("want refused")
	}
}
