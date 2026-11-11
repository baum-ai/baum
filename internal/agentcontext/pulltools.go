package agentcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The tools of the pull request page and the group page read the routes the
// app calls, through the call's Reader, and return what those routes answer,
// compacted. Each takes the repository and pull request it reads from
// CallContext.Target only.

// targetSchema is the schema of a tool whose only arguments are the
// repository and pull request, plus the properties extra adds ("" for none)
// and the names required lists.
func targetSchema(extra string, required ...string) json.RawMessage {
	properties := `"repository":{"type":"string","description":"The repository as owner/name. Defaults to the page's, or for an action its own."},` +
		`"pr":{"type":"integer","minimum":1,"description":"The pull request number. Defaults to the page's, or for an action its own."}`
	if extra != "" {
		properties += "," + extra
	}
	schema := `{"type":"object","properties":{` + properties + `},"additionalProperties":false`
	if len(required) > 0 {
		quoted, _ := json.Marshal(required)
		schema += `,"required":` + string(quoted)
	}
	return json.RawMessage(schema + "}")
}

// pullArgs are the arguments every pull request tool takes.
type pullArgs struct {
	Repository string `json:"repository"`
	PR         int    `json:"pr"`
}

// pullTarget resolves the repository and pull request a call reads.
func (c CallContext) pullTarget(args json.RawMessage) (string, int, error) {
	var a pullArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", 0, err
	}
	repository, number, err := c.Target(a.Repository, a.PR)
	if err != nil {
		return "", 0, err
	}
	if number <= 0 {
		return "", 0, errors.New("name a pull request: no page with one is open")
	}
	return repository, number, nil
}

// get reads path, with query, through the reader. A route that answers
// anything but 200 is an error carrying its message, apart from not found,
// which the second result reports so a caller can fall back.
func (c CallContext) get(ctx context.Context, path string, query url.Values) (json.RawMessage, bool, error) {
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	response, err := c.Reader.Get(ctx, target)
	if err != nil {
		return nil, false, err
	}
	switch {
	case response.Status == http.StatusNotFound:
		return nil, true, nil
	case response.Status != http.StatusOK:
		message := strings.TrimSpace(string(response.Body))
		if len(message) > 300 {
			message = message[:300]
		}
		return nil, false, fmt.Errorf("%s answered %d: %s: %w", path, response.Status, message, errStatus)
	}
	body := bytes.TrimSpace(response.Body)
	if !json.Valid(body) {
		return nil, false, fmt.Errorf("%s did not answer JSON", path)
	}
	return body, false, nil
}

// pullQuery is the repository and pr every GitHub-reading route takes.
func pullQuery(repository string, number int) url.Values {
	return url.Values{"repository": {repository}, "pr": {strconv.Itoa(number)}}
}

// compact is v as JSON without indentation.
func compactJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// reviewEntry is the review index's entry for a pull request, which the app
// shows its header from, and picks its clone and whether a review exists by.
type reviewEntry struct {
	raw          map[string]json.RawMessage
	Number       int
	HeadSHA      string
	Unresolved   *int
	CloneID      string
	NeedsClone   bool
	VersionCount int
}

// errStatus marks a route that answered something but 200.
var errStatus = errors.New("unexpected status")

// errIndexUnavailable says the daemon's review index cannot be read now, which
// is not the same as the pull request being absent from it.
var errIndexUnavailable = errors.New("the review index is unavailable: the daemon cannot list pull requests now")

// entry finds the pull request in the review index, as the app does with
// history on. Of the entries one pull request has in several clones, the one
// with a saved review speaks for it, else one with a clone, else the first.
func (c CallContext) entry(ctx context.Context, repository string, number int) (reviewEntry, error) {
	body, _, err := c.get(ctx, "/api/index", url.Values{"history": {"1"}})
	if err != nil {
		if errors.Is(err, errUnavailable) || errors.Is(err, errStatus) {
			return reviewEntry{}, fmt.Errorf("%w: %v", errIndexUnavailable, err)
		}
		return reviewEntry{}, err
	}
	var index struct {
		Available *bool                        `json:"available"`
		Entries   []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return reviewEntry{}, errors.New("the review index is not readable")
	}
	if index.Available != nil && !*index.Available {
		return reviewEntry{}, errIndexUnavailable
	}
	var found *reviewEntry
	for _, raw := range index.Entries {
		var pr struct {
			Repository string `json:"repository"`
			Number     int    `json:"number"`
			HeadSHA    string `json:"head_sha"`
			Unresolved *int   `json:"unresolved"`
		}
		if json.Unmarshal(raw["pull_request"], &pr) != nil || pr.Number != number || !strings.EqualFold(pr.Repository, repository) {
			continue
		}
		e := reviewEntry{raw: raw, Number: pr.Number, HeadSHA: pr.HeadSHA, Unresolved: pr.Unresolved}
		_ = json.Unmarshal(raw["clone_id"], &e.CloneID)
		_ = json.Unmarshal(raw["needs_clone"], &e.NeedsClone)
		_ = json.Unmarshal(raw["version_count"], &e.VersionCount)
		if found == nil || e.rank() > found.rank() {
			found = &e
		}
	}
	if found == nil {
		return reviewEntry{}, fmt.Errorf("%s#%d is not in the review index: it is not open in a repository baum watches", repository, number)
	}
	return *found, nil
}

// rank orders the entries of one pull request: a saved review first, then a
// clone.
func (e reviewEntry) rank() int {
	n := 0
	if e.VersionCount > 0 {
		n += 2
	}
	if e.CloneID != "" && !e.NeedsClone {
		n++
	}
	return n
}

// selector is what the app's clone-bound routes take for a pull request.
func (e reviewEntry) selector() (url.Values, error) {
	if e.CloneID == "" || e.NeedsClone {
		return nil, errors.New("this repository has no local clone, which this needs")
	}
	return url.Values{"clone": {e.CloneID}, "pr": {strconv.Itoa(e.Number)}}, nil
}

// pullSource is where the page reads a pull request's patch, commits and
// sessions from: its latest saved review when it has one, else its facts.
type pullSource struct {
	// Name says which: "review" or "facts".
	Name string
	// Prefix is the route prefix: /api for a review, /api/facts for facts.
	Prefix string
	// Query is the selector the routes take, with the facts' head.
	Query url.Values
	// Head is the head the files and patches are of.
	Head string
	// Hunks are the changed hunks the source lists.
	Hunks []sourceHunk
	// unbuilt marks facts not asked for yet: their routes may answer not found
	// until the daemon has built them.
	unbuilt bool
}

type sourceHunk struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Change    string `json:"change"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// sourceOf opens the pull request's source as the page does: the saved
// review's document when the pull request has a version, else the facts at
// the head the index shows, which the daemon builds when it is due.
func (c CallContext) sourceOf(ctx context.Context, e reviewEntry, files bool) (pullSource, error) {
	query, err := e.selector()
	if err != nil {
		return pullSource{}, err
	}
	path, name, prefix := "/api/facts", "facts", "/api/facts"
	if e.VersionCount > 0 {
		path, name, prefix = "/api/document", "review", "/api"
	} else if e.HeadSHA != "" {
		query.Set("head", e.HeadSHA)
	}
	if !files {
		// Commits and sessions need no files: a saved review's need nothing
		// from the document, and the facts are read at the head the index shows
		// without building them here (readSource builds them when the daemon
		// has none), so a large payload is not read for a head already known.
		return pullSource{Name: name, Prefix: prefix, Query: query, Head: e.HeadSHA, unbuilt: name == "facts"}, nil
	}
	body, missing, err := c.get(ctx, path, query)
	if err != nil {
		return pullSource{}, err
	}
	if missing {
		return pullSource{}, fmt.Errorf("the pull request's %s are not available", name)
	}
	var doc struct {
		Review struct {
			Head string `json:"head"`
		} `json:"review"`
		Index struct {
			Hunks []sourceHunk `json:"hunks"`
		} `json:"index"`
		// An unavailable answer of the facts route.
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return pullSource{}, fmt.Errorf("the pull request's %s are not readable", name)
	}
	if doc.Code != "" && doc.Review.Head == "" {
		return pullSource{}, fmt.Errorf("the pull request's facts are unavailable (%s): %s", doc.Code, doc.Message)
	}
	if name == "facts" {
		// A page names the head of the facts it shows.
		query.Set("head", doc.Review.Head)
	} else {
		query.Del("head")
	}
	return pullSource{Name: name, Prefix: prefix, Query: query, Head: doc.Review.Head, Hunks: doc.Index.Hunks}, nil
}

// route is the source's route name, with the query copied.
func (s pullSource) route(name string) (string, url.Values) {
	q := url.Values{}
	for k, v := range s.Query {
		q[k] = append([]string(nil), v...)
	}
	return s.Prefix + "/" + name, q
}
