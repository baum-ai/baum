package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The tools of the pull request and group pages beyond the ones that list
// what a page shows: a file at the review's head, one commit and its patches,
// the decisions captured for the pull request, what the app offers about the
// branch and the checks, the person's own draft review and walkthrough place,
// and a group's patches and files. Each is repository-scoped and bound to
// the action's repository in actionRoutes; the two kept-state tools are bound
// to the action's own pull request.

func (s *Server) pullExtraTools() []Tool {
	return []Tool{{
		Name: "file",
		Description: "A file of the repository at the head of the pull request's saved review, as the app's file view opens it from a context chip: the text (at most 512 KiB, cut with truncated set), or no content for a directory, a binary file or a path the head lacks. " +
			"Only a file the review cites can be read, and only a pull request with a saved review has one.",
		InputSchema: targetSchema(`"path":{"type":"string","minLength":1,"maxLength":1000,"description":"The file's path in the repository, as the review cites it."}`, "path"),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/index", "/api/file"},
		Call:        callFile,
	}, {
		Name:        "commit",
		Description: "One of the pull request's commits as the app's commit view shows it: the message body and the files it changed (path, old path, status, lines added and deleted). Read from the saved review when there is one, else the facts. The commit must be one commits lists.",
		InputSchema: targetSchema(`"oid":{"type":"string","pattern":"^[0-9a-f]{7,64}$","description":"The commit id, from commits."}`, "oid"),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/index", "/api/facts", "/api/commit"},
		Call:        callCommit,
	}, {
		Name:        "commit_patch",
		Description: "One changed file's patch in one of the pull request's commits, as the app's commit view shows it. The commit must be one commits lists and the path one of its files (commit).",
		InputSchema: targetSchema(`"oid":{"type":"string","pattern":"^[0-9a-f]{7,64}$","description":"The commit id, from commits."},"path":{"type":"string","minLength":1,"maxLength":1000,"description":"A file the commit changed."}`, "oid", "path"),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/index", "/api/facts", "/api/commit-patch"},
		Call:        callCommitPatch,
	}, {
		Name: "pull_request_decisions",
		Description: "The decisions captured from the agent sessions of the pull request, as its page lists them under Decisions: each linked session (agent, whether it is live, whether capture ran) with its decisions, and which were made after the saved review was generated (provisional). " +
			"The Timeline's decisions tool lists decisions across a repository instead.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/decisions"},
		Call:        callPullDecisions,
	}, {
		Name: "check_log",
		Description: "The last lines of a failing check's log, as the pull request page shows them under the check (at most 200 lines and 64 KiB, with timestamps and colours removed, cut with truncated set), read from GitHub when asked: only a completed GitHub Actions check has a log, and GitHub drops old ones. " +
			"The check is one of the pull request's current checks (checks), named by its name or its id.",
		InputSchema: targetSchema(`"check":{"type":["string","integer"],"minLength":1,"maxLength":300,"description":"The check's name, or its id, as checks lists them."}`, "check"),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/index", "/api/prs/checks", "/api/prs/check-log"},
		Call:        callCheckLog,
	}, {
		Name:        "update_branch_options",
		Description: "What the app offers about bringing the pull request's branch up to date: whether it is behind or conflicts and which update methods apply. Nothing is changed.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/prs/update-branch-options"},
		Call:        callPullOption("/api/prs/update-branch-options"),
	}, {
		Name:        "rerun_options",
		Description: "What the app offers about re-running the pull request's failed checks: the failed workflow runs that can be re-run. Nothing is changed.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/prs/rerun-checks-options"},
		Call:        callPullOption("/api/prs/rerun-checks-options"),
	}, {
		Name: "pending_review",
		Description: "The person's own draft review of the pull request, as the app's pending review lists it: each comment with what it is about, its text, where in the diff it sits and the head it was written on, the review's summary note, and the reviews already sent from baum. " +
			"It is the person's unsent writing: read it to help them with it, never to quote it elsewhere.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/ui-state/review"},
		Call:        callPendingReview,
	}, {
		Name: "walkthrough_position",
		Description: "Where the person is in the walkthrough of the pull request's saved review: the stops they have seen, what they said looks off at each, and their overall comment. " +
			"Defaults to the latest review version.",
		InputSchema: targetSchema(`"version":{"type":"integer","minimum":1,"description":"The review version. Default: the latest."}`),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/index", "/api/ui-state/walkthrough"},
		Call:        callWalkthrough,
	}, {
		Name:        "group_patch",
		Description: "The patch of a file a group's combined review cites, from the member whose comparison changes it, as the group page shows it; hunk names the cited hunk when several members change the file. The path must be one the review cites. Defaults to the latest version.",
		InputSchema: groupExtraSchema(`"path":{"type":"string","minLength":1,"maxLength":1000,"description":"The file's path, as the review cites it."},"hunk":{"type":"string","maxLength":200,"description":"The cited hunk's id."}`, "path"),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/groups"},
		Call:        callGroupExtra("patch"),
	}, {
		Name:        "group_file",
		Description: "A file of the repository at a group review's head, as the group page's file view opens it: the text (at most 512 KiB), or no content for a directory, a binary file or a path the head lacks. The path must be one the review cites. Defaults to the latest version.",
		InputSchema: groupExtraSchema(`"path":{"type":"string","minLength":1,"maxLength":1000,"description":"The file's path, as the review cites it."}`, "path"),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/groups"},
		Call:        callGroupExtra("file"),
	}}
}

// groupExtraSchema is the schema of a group tool: the group, a version and
// the properties extra adds.
func groupExtraSchema(extra string, required ...string) json.RawMessage {
	quoted, _ := json.Marshal(required)
	return json.RawMessage(`{"type":"object","properties":{` +
		`"repository":{"type":"string","description":"The group's repository as owner/name. Defaults to the page's, or for an action its own."},` +
		`"id":{"type":"string","description":"The group's id. Defaults to the open group page's, or for an action the one group its pull request is in."},` +
		`"version":{"type":"integer","minimum":1,"description":"The review version. Default: the latest."},` + extra + `},"required":` + string(quoted) + `,"additionalProperties":false}`)
}

var oidPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

func pathArg(args json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", errors.New("name a path")
	}
	return a.Path, nil
}

func oidArg(args json.RawMessage) (string, error) {
	var a struct {
		OID string `json:"oid"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if !oidPattern.MatchString(a.OID) {
		return "", errors.New("oid is not a commit id")
	}
	return a.OID, nil
}

func callFile(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	path, err := pathArg(args)
	if err != nil {
		return "", err
	}
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	if e.VersionCount == 0 {
		return "", errors.New("the pull request has no saved review: only a saved review cites files")
	}
	query, err := e.selector()
	if err != nil {
		return "", err
	}
	query.Set("path", path)
	body, missing, err := c.get(ctx, "/api/file", query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", fmt.Errorf("%s is not a file the pull request's review cites", path)
	}
	return string(body), nil
}

func callCommit(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	oid, err := oidArg(args)
	if err != nil {
		return "", err
	}
	body, err := readSourceWith(ctx, c, args, "commit", url.Values{"oid": {oid}})
	if err != nil {
		return "", fmt.Errorf("%w (is it one of commits' ids?)", err)
	}
	return string(body), nil
}

func callCommitPatch(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	oid, err := oidArg(args)
	if err != nil {
		return "", err
	}
	path, err := pathArg(args)
	if err != nil {
		return "", err
	}
	body, err := readSourceWith(ctx, c, args, "commit-patch", url.Values{"oid": {oid}, "path": {path}})
	if err != nil {
		return "", fmt.Errorf("%w (is the commit one of commits' ids, and the path one it changed?)", err)
	}
	return string(body), nil
}

func callPullDecisions(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	body, _, err := c.get(ctx, "/api/decisions", pullQuery(repository, number))
	if err != nil {
		return "", err
	}
	if disabled(body) {
		return "", errors.New("decisions are unavailable: the daemon does not capture them")
	}
	return string(body), nil
}

func callPullOption(path string) func(context.Context, CallContext, json.RawMessage) (string, error) {
	return func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
		repository, number, err := c.pullTarget(args)
		if err != nil {
			return "", err
		}
		body, _, err := c.get(ctx, path, pullQuery(repository, number))
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

// callCheckLog reads the log tail of one of the pull request's checks, as the
// page does: the checks are read first, as the page lists them, so the check
// named is one of the pull request's, and the log route is asked only for a
// check run the list has.
func callCheckLog(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	var a struct {
		Check json.RawMessage `json:"check"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	var name string
	var id int64
	if json.Unmarshal(a.Check, &name) != nil {
		var n json.Number
		if err := json.Unmarshal(a.Check, &n); err != nil {
			return "", errors.New("name a check")
		}
		if id, err = n.Int64(); err != nil || id < 1 {
			return "", errors.New("check is not a check id")
		}
	} else if name = strings.TrimSpace(name); name == "" {
		return "", errors.New("name a check")
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	query := pullQuery(repository, number)
	query.Set("head_sha", e.HeadSHA)
	body, missing, err := c.get(ctx, "/api/prs/checks", query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("the pull request's checks are not available")
	}
	var list struct {
		Checks []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", errors.New("the checks are not readable")
	}
	var matches []int
	for i, check := range list.Checks {
		if (id > 0 && check.ID == id) || (id == 0 && check.Name == name) {
			matches = append(matches, i)
		}
	}
	switch {
	case len(matches) == 0:
		return "", errors.New("that is not one of the pull request's checks: name or number one that checks lists")
	case len(matches) > 1:
		return "", errors.New("several checks have that name: give the check's id")
	}
	check := list.Checks[matches[0]]
	if check.ID == 0 {
		// A commit status: GitHub keeps no log, and the route names check runs.
		return compactJSON(map[string]any{"name": check.Name, "available": false, "reason": "no_log",
			"message": "GitHub keeps no log for this check: it is a commit status, not a GitHub Actions run."})
	}
	query.Del("head_sha")
	query.Set("check", strconv.FormatInt(check.ID, 10))
	body, missing, err = c.get(ctx, "/api/prs/check-log", query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("the check's log is not available")
	}
	return string(body), nil
}

// reviewKey is the key the app keeps a pull request's draft review under
// (pendingReviewKey in the app): the repository in lower case, #, the number.
func reviewKey(repository string, number int) string {
	return strings.ToLower(repository) + "#" + strconv.Itoa(number)
}

// walkthroughKey is the key of a review version's walkthrough progress
// (progressKey in the app).
func walkthroughKey(repository string, number, version int) string {
	return reviewKey(repository, number) + "@v" + strconv.Itoa(version)
}

// ownReviewKey reports whether key is the draft review of the action's own
// pull request: nothing else is.
func ownReviewKey(key, repository string, pull int) bool {
	return key == reviewKey(repository, pull)
}

// ownWalkthroughKey reports whether key is a walkthrough progress key of the
// action's own pull request, at any version.
func ownWalkthroughKey(key, repository string, pull int) bool {
	rest, ok := strings.CutPrefix(key, reviewKey(repository, pull)+"@v")
	if !ok || rest == "" {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n > 0 && strconv.Itoa(n) == rest
}

// savedState reads the value of a kept UI state, null for none.
func (c CallContext) savedState(ctx context.Context, kind, key string) (json.RawMessage, error) {
	body, missing, err := c.get(ctx, "/api/ui-state/"+kind, url.Values{"key": {key}})
	if err != nil {
		return nil, err
	}
	if missing || disabled(body) {
		return nil, errors.New("the app's saved state is unavailable")
	}
	var record struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(body, &record) != nil {
		return nil, errors.New("the saved state is not readable")
	}
	return record.Value, nil
}

func callPendingReview(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	value, err := c.savedState(ctx, "review", reviewKey(repository, number))
	if err != nil {
		return "", err
	}
	var saved struct {
		Comments []struct {
			Target  json.RawMessage `json:"target"`
			Body    string          `json:"body"`
			Place   json.RawMessage `json:"place"`
			Head    string          `json:"head"`
			Created int64           `json:"created"`
		} `json:"comments"`
		Note string `json:"note"`
		Sent []struct {
			URL      string          `json:"url"`
			Event    string          `json:"event"`
			At       int64           `json:"at"`
			Head     string          `json:"head,omitempty"`
			Note     string          `json:"note"`
			Comments json.RawMessage `json:"comments"`
		} `json:"sent"`
	}
	if len(value) > 0 && string(value) != "null" {
		if err := json.Unmarshal(value, &saved); err != nil {
			return "", errors.New("the draft review is not readable")
		}
	}
	type comment struct {
		Target  json.RawMessage `json:"target"`
		Body    string          `json:"body"`
		Place   json.RawMessage `json:"place,omitempty"`
		Head    string          `json:"head,omitempty"`
		Created int64           `json:"created"`
	}
	type sent struct {
		URL      string          `json:"url"`
		Event    string          `json:"event"`
		At       int64           `json:"at"`
		Head     string          `json:"head,omitempty"`
		Note     string          `json:"note,omitempty"`
		Comments json.RawMessage `json:"comments,omitempty"`
	}
	out := struct {
		Repository string    `json:"repository"`
		Number     int       `json:"number"`
		Comments   []comment `json:"comments"`
		Note       string    `json:"note"`
		Sent       []sent    `json:"sent"`
	}{Repository: repository, Number: number, Comments: []comment{}, Note: saved.Note, Sent: []sent{}}
	for _, k := range saved.Comments {
		place := k.Place
		if string(place) == "null" {
			place = nil
		}
		out.Comments = append(out.Comments, comment{k.Target, k.Body, place, k.Head, k.Created})
	}
	for _, r := range saved.Sent {
		out.Sent = append(out.Sent, sent{r.URL, r.Event, r.At, r.Head, r.Note, r.Comments})
	}
	return compactJSON(out)
}

func callWalkthrough(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	version := a.Version
	if version == 0 {
		e, err := c.entry(ctx, repository, number)
		if err != nil {
			return "", err
		}
		var latest struct {
			Version int `json:"version"`
		}
		_ = json.Unmarshal(e.raw["latest"], &latest)
		if version = latest.Version; version == 0 {
			return "", errors.New("the pull request has no saved review, so no walkthrough")
		}
	}
	value, err := c.savedState(ctx, "walkthrough", walkthroughKey(repository, number, version))
	if err != nil {
		return "", err
	}
	progress := struct {
		Seen  []string          `json:"seen"`
		Notes map[string]string `json:"notes"`
		Note  string            `json:"note"`
	}{Seen: []string{}, Notes: map[string]string{}}
	if len(value) > 0 && string(value) != "null" {
		if err := json.Unmarshal(value, &progress); err != nil {
			return "", errors.New("the walkthrough's progress is not readable")
		}
	}
	return compactJSON(struct {
		Repository string            `json:"repository"`
		Number     int               `json:"number"`
		Version    int               `json:"version"`
		Seen       []string          `json:"seen"`
		Notes      map[string]string `json:"notes"`
		Note       string            `json:"note"`
	}{repository, number, version, emptyList(progress.Seen), progress.Notes, progress.Note})
}

func emptyList[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// callGroupExtra reads one of a group's file routes (patch, file) for the
// group the call names, after checking the group is the call's repository's.
func callGroupExtra(route string) func(context.Context, CallContext, json.RawMessage) (string, error) {
	return func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
		var a struct {
			Version int    `json:"version"`
			Path    string `json:"path"`
			Hunk    string `json:"hunk"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		if a.Path == "" {
			return "", errors.New("name a path")
		}
		id, _, err := groupOf(ctx, c, args)
		if err != nil {
			return "", err
		}
		query := url.Values{"path": {a.Path}}
		if a.Version > 0 {
			query.Set("version", strconv.Itoa(a.Version))
		}
		if a.Hunk != "" && route == "patch" {
			query.Set("hunk", a.Hunk)
		}
		body, missing, err := c.get(ctx, "/api/groups/"+url.PathEscape(id)+"/"+route, query)
		if err != nil {
			return "", err
		}
		if missing {
			return "", fmt.Errorf("%s is not a file the group's review cites", a.Path)
		}
		return string(body), nil
	}
}
