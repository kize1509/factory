// Package youtrack reads issues from YouTrack's REST API, so a job can
// start from a ticket ID alone.
package youtrack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	URL   string // e.g. https://yourcompany.youtrack.cloud
	Token string // permanent token, perm:…
}

type Issue struct {
	ID          string `json:"idReadable"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Project     struct {
		ShortName string `json:"shortName"`
	} `json:"project"`
}

func (c Client) Configured() bool { return c.URL != "" && c.Token != "" }

// Issue loads one issue by its readable ID, like ABC-123.
func (c Client) Issue(ctx context.Context, id string) (Issue, error) {
	var is Issue
	u := strings.TrimRight(c.URL, "/") + "/api/issues/" + url.PathEscape(id) + "?fields=idReadable,summary,description,project(shortName)"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return is, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return is, fmt.Errorf("YouTrack: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == 404:
		return is, fmt.Errorf("YouTrack has no issue %s", id)
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return is, fmt.Errorf("YouTrack refused the token (%d); check it in Admin → Connections", resp.StatusCode)
	case resp.StatusCode != 200:
		return is, fmt.Errorf("YouTrack answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return is, json.Unmarshal(body, &is)
}
