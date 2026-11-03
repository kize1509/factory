// Package youtrack reads issues from YouTrack's REST API, so a job can
// start from a ticket ID alone.
package youtrack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	CustomFields []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"customFields"`
}

// ShipModeField is the ticket field that says how a job ships: Commit,
// Push or PR.
const ShipModeField = "Ship mode"

// field returns the name of a single-value field, or "" when it is empty
// or missing.
func (is Issue) field(name string) string {
	for _, f := range is.CustomFields {
		if f.Name != name {
			continue
		}
		var v struct {
			Name string `json:"name"`
		}
		json.Unmarshal(f.Value, &v) // null stays empty
		return strings.TrimSpace(v.Name)
	}
	return ""
}

// ShipMode is the ticket's ship mode in lower case (commit, push or pr),
// or "" when the ticket doesn't set one.
func (is Issue) ShipMode() string { return strings.ToLower(is.field(ShipModeField)) }

func (c Client) Configured() bool { return c.URL != "" && c.Token != "" }

// Issue loads one issue by its readable ID, like ABC-123.
func (c Client) Issue(ctx context.Context, id string) (Issue, error) {
	var is Issue
	u := strings.TrimRight(c.URL, "/") + "/api/issues/" + url.PathEscape(id) + "?fields=idReadable,summary,description,project(shortName),customFields(name,value(name))"
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

// ---- reading and writing for the operations agent ----

// do sends one request and returns the body, turning the usual failures
// into plain messages.
func (c Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("YouTrack: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, fmt.Errorf("YouTrack refused the token (%d); check it in Admin → Connections", resp.StatusCode)
	case resp.StatusCode == 404:
		return nil, fmt.Errorf("YouTrack has nothing at %s", path)
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("YouTrack answered %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c Client) postJSON(ctx context.Context, path string, v any) ([]byte, error) {
	b, _ := json.Marshal(v)
	return c.do(ctx, "POST", path, strings.NewReader(string(b)), "application/json")
}

// Brief is one line of a search result.
type Brief struct {
	ID      string `json:"idReadable"`
	Summary string `json:"summary"`
	State   string `json:"state,omitempty"`
}

// Search runs a YouTrack query, like "project: HF #Unresolved login".
func (c Client) Search(ctx context.Context, query string, max int) ([]Brief, error) {
	if max <= 0 || max > 50 {
		max = 20
	}
	q := url.Values{"query": {query}, "$top": {fmt.Sprint(max)},
		"fields": {"idReadable,summary,customFields(name,value(name))"}}
	data, err := c.do(ctx, "GET", "/api/issues?"+q.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	var raw []Issue
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]Brief, len(raw))
	for i, is := range raw {
		out[i] = Brief{ID: is.ID, Summary: is.Summary, State: is.field("State")}
	}
	return out, nil
}

// Comment is one comment on an issue.
type Comment struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Created int64  `json:"created"` // milliseconds since 1970
	Author  struct {
		Login string `json:"login"`
		Name  string `json:"fullName"`
	} `json:"author"`
}

// Link is a link between two issues, as seen from one of them.
type Link struct {
	Type   string   `json:"type"` // e.g. "Subtask", "Relates", "Duplicate"
	Dir    string   `json:"direction"`
	Issues []string `json:"issues"`
}

// Detail is an issue read in full.
type Detail struct {
	Issue
	Tags        []string
	Fields      map[string]string
	Comments    []Comment
	Links       []Link
	Attachments []string
	Activity    []string // recent changes, one line each
}

const readFields = "idReadable,summary,description,project(shortName),tags(name)," +
	"customFields(name,value(name,login)),attachments(name),comments(id,text,created,author(login,fullName))," +
	"links(direction,linkType(name),issues(idReadable))"

// Read loads an issue with its comments, links, attachments, tags and the
// last changes made to it.
func (c Client) Read(ctx context.Context, id string) (Detail, error) {
	var d Detail
	data, err := c.do(ctx, "GET", "/api/issues/"+url.PathEscape(id)+"?fields="+url.QueryEscape(readFields), nil, "")
	if err != nil {
		return d, err
	}
	var raw struct {
		Issue
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
		Attachments []struct {
			Name string `json:"name"`
		} `json:"attachments"`
		Comments []Comment `json:"comments"`
		Links    []struct {
			Dir  string `json:"direction"`
			Type struct {
				Name string `json:"name"`
			} `json:"linkType"`
			Issues []struct {
				ID string `json:"idReadable"`
			} `json:"issues"`
		} `json:"links"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return d, err
	}
	d.Issue, d.Comments, d.Fields = raw.Issue, raw.Comments, map[string]string{}
	for _, cf := range raw.CustomFields {
		var v struct {
			Name  string `json:"name"`
			Login string `json:"login"`
		}
		json.Unmarshal(cf.Value, &v)
		if n := firstNonEmpty(v.Name, v.Login); n != "" {
			d.Fields[cf.Name] = n
		}
	}
	for _, t := range raw.Tags {
		d.Tags = append(d.Tags, t.Name)
	}
	for _, a := range raw.Attachments {
		d.Attachments = append(d.Attachments, a.Name)
	}
	for _, l := range raw.Links {
		if len(l.Issues) == 0 {
			continue
		}
		ln := Link{Type: l.Type.Name, Dir: l.Dir}
		for _, is := range l.Issues {
			ln.Issues = append(ln.Issues, is.ID)
		}
		d.Links = append(d.Links, ln)
	}
	// history is a nice to have: an error here doesn't fail the read
	if act, err := c.do(ctx, "GET", "/api/issues/"+url.PathEscape(id)+"/activities?categories=CustomFieldCategory,CommentsCategory&$top=15&fields="+url.QueryEscape("timestamp,author(login),field(name),added(name,text),category(id)"), nil, ""); err == nil {
		var as []struct {
			At     int64 `json:"timestamp"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Field struct {
				Name string `json:"name"`
			} `json:"field"`
			Added json.RawMessage `json:"added"`
		}
		if json.Unmarshal(act, &as) == nil {
			for _, a := range as {
				d.Activity = append(d.Activity, fmt.Sprintf("%s %s changed %s", time.UnixMilli(a.At).UTC().Format("2006-01-02 15:04"), a.Author.Login, a.Field.Name))
			}
		}
	}
	return d, nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ProjectID turns a project's short name (HF) into the internal id that
// creating an issue needs.
func (c Client) ProjectID(ctx context.Context, short string) (string, error) {
	data, err := c.do(ctx, "GET", "/api/admin/projects?fields=id,shortName&query="+url.QueryEscape(short), nil, "")
	if err != nil {
		return "", err
	}
	var ps []struct{ ID, ShortName string }
	if err := json.Unmarshal(data, &ps); err != nil {
		return "", err
	}
	for _, p := range ps {
		if strings.EqualFold(p.ShortName, short) {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("YouTrack has no project %s", short)
}

// NewIssue is what Create takes. Fields beyond summary and description are
// applied afterwards with typed commands.
type NewIssue struct {
	Project     string
	Summary     string
	Description string
}

// Create opens an issue and returns its readable ID.
func (c Client) Create(ctx context.Context, n NewIssue) (string, error) {
	pid, err := c.ProjectID(ctx, n.Project)
	if err != nil {
		return "", err
	}
	data, err := c.postJSON(ctx, "/api/issues?fields=idReadable", map[string]any{
		"project": map[string]string{"id": pid}, "summary": n.Summary, "description": n.Description})
	if err != nil {
		return "", err
	}
	var is Issue
	if err := json.Unmarshal(data, &is); err != nil || is.ID == "" {
		return "", fmt.Errorf("YouTrack created the issue but returned no ID")
	}
	return is.ID, nil
}

// Command applies one YouTrack command ("State In Progress", "tag x",
// "relates to HF-3") to issues. The caller builds it from typed fields.
func (c Client) Command(ctx context.Context, ids []string, command string) error {
	var is []map[string]string
	for _, id := range ids {
		is = append(is, map[string]string{"idReadable": id})
	}
	_, err := c.postJSON(ctx, "/api/commands", map[string]any{"query": command, "issues": is})
	return err
}

// AddComment adds a comment to an issue.
func (c Client) AddComment(ctx context.Context, id, text string) error {
	_, err := c.postJSON(ctx, "/api/issues/"+url.PathEscape(id)+"/comments", map[string]string{"text": text})
	return err
}

// Comments lists an issue's comments newer than since (milliseconds).
func (c Client) Comments(ctx context.Context, id string, since int64) ([]Comment, error) {
	data, err := c.do(ctx, "GET", "/api/issues/"+url.PathEscape(id)+"/comments?fields="+url.QueryEscape("id,text,created,author(login,fullName)")+"&$top=100", nil, "")
	if err != nil {
		return nil, err
	}
	var all []Comment
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var out []Comment
	for _, cm := range all {
		if cm.Created > since {
			out = append(out, cm)
		}
	}
	return out, nil
}

// Attach uploads a file to an issue.
func (c Client) Attach(ctx context.Context, id, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	mw.Close()
	_, err = c.do(ctx, "POST", "/api/issues/"+url.PathEscape(id)+"/attachments?fields=id", &buf, mw.FormDataContentType())
	return err
}

// Quote makes a value safe inside a YouTrack command: braces keep spaces
// together, and anything that could end the value or start a new command
// is refused.
func Quote(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("empty value")
	}
	if strings.ContainsAny(v, "{}\n\r") {
		return "", fmt.Errorf("value %q has characters a command can't carry", v)
	}
	if strings.ContainsAny(v, " :,") {
		return "{" + v + "}", nil
	}
	return v, nil
}
