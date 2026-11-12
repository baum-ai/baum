package agentcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
)

// maxRead bounds the body of one read.
const maxRead = 8 << 20

// maxDocumentRead bounds the body of a read of a review document or of the
// facts and a running review's draft, which the daemon serves up to viewserver.MaxDocumentBytes (a test
// keeps the two equal). Only those routes may be that large; a tool
// projects what it needs, so the result's own bounds are unchanged.
const maxDocumentRead = 32 << 20

// readLimit is the body bound of a read of path.
func readLimit(path string) int {
	if path == "/api/document" || path == "/api/facts" ||
		(strings.HasPrefix(path, "/api/groups/") && strings.HasSuffix(path, "/document")) ||
		(strings.HasPrefix(path, "/api/jobs/") && strings.HasSuffix(path, "/draft")) {
		return maxDocumentRead
	}
	return maxRead
}

// unreadable are the routes no tool may read, whatever it declares: settings,
// credentials and connections, diagnostics, plan usage, the event stream and
// the MCP route itself. Each is a plain string prefix, so /api/github covers
// /api/github-auth and /api/github/.
var unreadable = []string{"/api/settings", "/api/debug", "/api/github", "/api/diagnostics", "/api/plan-usage", "/api/events", "/api/mcp"}

// projectedReads are the only exceptions to unreadable: the tools that may read
// one exact unreadable route, in process, by name. Each returns a typed
// projection of the answer, never the answer itself, so what the route holds
// beyond what the app shows on that page (paths, home, environment, sign-in
// state) never reaches an agent. The route is matched whole (not as a prefix),
// only the tool named may declare it (Tool.Projected, checked when the tool is
// compiled), only a ScopeGlobal tool may, and an action's reader refuses it
// whatever a tool declares. A test lists these pairs exactly.
//
// A route under an unreadable prefix is still unreadable (/api/settings/
// appearance is under /api/settings), so each Settings route a tool reads is
// named here on its own.
var projectedReads = map[string][]string{
	"diagnostics_summary":  {"/api/diagnostics"},
	"settings_general":     {"/api/settings/appearance"},
	"settings_reviews":     {"/api/settings"},
	"settings_fixes":       {"/api/settings"},
	"settings_connections": {"/api/github-auth"},
	"settings_computer":    {"/api/settings", "/api/diagnostics"},
	"prompts":              {"/api/settings/prompts"},
	"prompt":               {"/api/settings/prompts"},
	// The operations that start a paid run read the harness, model and
	// effort it will use, for the summary the person confirms; the agent
	// gets a receipt.
	"propose_review": {"/api/settings"},
	"propose_fix":    {"/api/settings"},
}

// checkProjected refuses a tool a projected route it was not allowed.
func checkProjected(name string, scope Scope, routes []string) error {
	for _, route := range routes {
		if !slices.Contains(projectedReads[name], route) {
			return fmt.Errorf("%s may not read %s: it is not in projectedReads", name, route)
		}
		if scope != ScopeGlobal {
			return fmt.Errorf("%s reads %s: only a global tool may read a projected route", name, route)
		}
	}
	return nil
}

// refreshFlags are query parameters that make a GET route do work beyond
// reading (refresh a cache, fetch more from GitHub); no read sends them.
var refreshFlags = []string{"refresh", "more"}

// binding is how a route names the repository it serves.
type binding int

const (
	// byRepository: the handler reads the repository= parameter.
	byRepository binding = iota + 1
	// byClone: the handler reads clone=, a registered clone resolved to its
	// repository.
	byClone
	// byPath: the path holds {owner} and {name}.
	byPath
	// byResponse: the route finds its data by id, so the repository is read
	// from the response (actionRoute.check) and nothing is returned on a
	// mismatch.
	byResponse
	// byLookup: the route finds its data by id but its response names no
	// repository, so the reader first reads the record the id names
	// (actionRoute.lookup), checks its repository (actionRoute.check) and
	// reads nothing on a mismatch.
	byLookup
	// byFilter: the route answers for every repository; the reader keeps
	// only the bound repository's records (actionRoute.filter) and the agent
	// never sees the rest.
	byFilter
	// byKey: the route is the saved UI state, named by a key (key=); the
	// handler cannot tell whose it is, so the reader requires the key to be
	// the action's own pull request's (actionRoute.key).
	byKey
)

// actionRoute is a route an action may read and how it is bound to the
// action's repository.
type actionRoute struct {
	// pattern is the path, with {name} standing for one whole segment.
	pattern string
	bind    binding
	// params are the query parameters allowed besides the binding one; any
	// other is refused.
	params []string
	// check reads the repository from a byResponse or byLookup record.
	check func(body []byte) (string, error)
	// lookup is the path of the record a byLookup route's id names.
	lookup func(vars map[string]string) string
	// filter keeps only repository's records of a byFilter route's body.
	filter func(body []byte, repository string) ([]byte, error)
	// key reports whether a byKey route's key= names the pull request.
	key func(key, repository string, pull int) bool
}

// actionRoutes are the only routes an action reads; any other is refused to
// it. An entry binds the route the way its handler finds what it serves.
var actionRoutes = []actionRoute{
	// The jobs of one pull request: the handler reads repository and pr.
	{pattern: "/api/jobs", bind: byRepository, params: []string{"pr"}},
	// One job, by id, and its transcript, which names no repository: the job
	// it belongs to is read first.
	{pattern: "/api/jobs/{id}", bind: byResponse, check: repositoryField},
	{pattern: "/api/jobs/{id}/draft", bind: byLookup, check: repositoryField, lookup: func(v map[string]string) string { return "/api/jobs/" + v["id"] }},
	{pattern: "/api/jobs/{id}/transcript", bind: byLookup, params: []string{"after"}, check: repositoryField, lookup: func(v map[string]string) string { return "/api/jobs/" + v["id"] }},

	// The pull request page's reads of GitHub, by repository and number.
	// A pull request's conversations with its review's agent, by clone.
	{pattern: "/api/prs/chat", bind: byClone, params: []string{"pr"}},
	{pattern: "/api/prs/description", bind: byRepository, params: []string{"pr"}},
	{pattern: "/api/prs/checks", bind: byRepository, params: []string{"pr", "head_sha"}},
	{pattern: "/api/prs/failing-checks", bind: byRepository, params: []string{"pr", "head_sha"}},
	{pattern: "/api/prs/review-threads", bind: byRepository, params: []string{"pr", "unresolved"}},
	{pattern: "/api/prs/conflicts", bind: byRepository, params: []string{"pr"}},
	{pattern: "/api/prs/merge-options", bind: byRepository, params: []string{"pr"}},
	{pattern: "/api/prs/update-branch-options", bind: byRepository, params: []string{"pr"}},
	{pattern: "/api/prs/rerun-checks-options", bind: byRepository, params: []string{"pr"}},

	// The review index and the inbox answer for every repository: the
	// reader keeps the action's repository's records.
	{pattern: "/api/index", bind: byFilter, params: []string{"history"}, filter: filterIndex},
	{pattern: "/api/inbox", bind: byFilter, filter: filterInbox},

	// A pull request's saved review and what the app reads of it, by the
	// registered clone the handler resolves.
	{pattern: "/api/versions", bind: byClone, params: []string{"pr"}},
	{pattern: "/api/document", bind: byClone, params: []string{"pr", "version"}},
	{pattern: "/api/patch", bind: byClone, params: []string{"pr", "version", "path"}},
	{pattern: "/api/commits", bind: byClone, params: []string{"pr", "version"}},
	{pattern: "/api/sessions", bind: byClone, params: []string{"pr", "version"}},
	{pattern: "/api/sessions/{id}", bind: byClone, params: []string{"pr", "version"}},
	// The files and commits the review cites, and the decisions captured
	// for the pull request (by repository and number: the handler's other
	// form, by session, names no repository and is not allowed).
	{pattern: "/api/file", bind: byClone, params: []string{"pr", "version", "path"}},
	{pattern: "/api/commit", bind: byClone, params: []string{"pr", "version", "oid"}},
	{pattern: "/api/commit-patch", bind: byClone, params: []string{"pr", "version", "oid", "path"}},
	{pattern: "/api/decisions", bind: byRepository, params: []string{"pr"}},

	// The person's own draft review and walkthrough place are kept by key,
	// which says whose they are: only the action's own pull request's key.
	{pattern: "/api/ui-state/review", bind: byKey, key: ownReviewKey},
	{pattern: "/api/ui-state/walkthrough", bind: byKey, key: ownWalkthroughKey},

	// Its facts, before a review exists.
	{pattern: "/api/facts", bind: byClone, params: []string{"pr", "head"}},
	{pattern: "/api/facts/patch", bind: byClone, params: []string{"pr", "head", "path"}},
	{pattern: "/api/facts/commits", bind: byClone, params: []string{"pr", "head"}},
	{pattern: "/api/facts/sessions", bind: byClone, params: []string{"pr", "head"}},
	{pattern: "/api/facts/sessions/{id}", bind: byClone, params: []string{"pr", "head"}},
	{pattern: "/api/facts/commit", bind: byClone, params: []string{"pr", "head", "oid"}},
	{pattern: "/api/facts/commit-patch", bind: byClone, params: []string{"pr", "head", "oid", "path"}},

	// A review group, by id; its versions name no repository, so the group
	// is read first.
	{pattern: "/api/groups", bind: byFilter, filter: filterGroups},
	{pattern: "/api/groups/{id}", bind: byResponse, check: repositoryField},
	{pattern: "/api/groups/{id}/document", bind: byLookup, params: []string{"version"}, check: repositoryField, lookup: func(v map[string]string) string { return "/api/groups/" + v["id"] }},
	{pattern: "/api/groups/{id}/versions", bind: byLookup, check: repositoryField, lookup: func(v map[string]string) string { return "/api/groups/" + v["id"] }},
	{pattern: "/api/groups/{id}/patch", bind: byLookup, params: []string{"version", "path", "hunk"}, check: repositoryField, lookup: func(v map[string]string) string { return "/api/groups/" + v["id"] }},
	{pattern: "/api/groups/{id}/file", bind: byLookup, params: []string{"version", "path"}, check: repositoryField, lookup: func(v map[string]string) string { return "/api/groups/" + v["id"] }},

	// The Timeline's lists name the repository (the handler reads every
	// repository= value and serves only those), and its details name it in
	// their responses. The people lists and a session's transcript are not
	// here: the first names no repository, the second nothing that says whose
	// it is.
	{pattern: "/api/timeline/decisions", bind: byRepository, params: []string{"q", "who", "category", "file", "person", "superseded", "from", "to", "limit", "cursor"}},
	{pattern: "/api/timeline/decisions/{id}", bind: byResponse, check: timelineRepositoryField},
	{pattern: "/api/timeline/features", bind: byRepository, params: []string{"q", "status", "file", "person", "from", "to", "limit", "cursor"}},
	{pattern: "/api/timeline/features/{id}", bind: byResponse, check: timelineRepositoryField},
	{pattern: "/api/timeline/build", bind: byRepository},
	{pattern: "/api/timeline/files", bind: byRepository, params: []string{"prefix", "limit"}},

	// The running sessions answer for every repository: the reader keeps the
	// action's repository's.
	{pattern: "/api/live-sessions", bind: byFilter, filter: filterLiveSessions},

	// A repository's page: its settings, clones and earlier names, and its
	// automatic review state, from lists of every repository the reader
	// rebuilds from the action's repository's row alone; and its prompts, by
	// path.
	{pattern: "/api/repositories", bind: byFilter, filter: filterRepositories},
	{pattern: "/api/automatic", bind: byFilter, filter: filterAutomatic},
	{pattern: "/api/repositories/{owner}/{name}/prompts", bind: byPath},
}

// filterRepositories keeps the row of repository in an /api/repositories
// body, rebuilt from its settings, its clones' ids and availability and its
// earlier names: a clone's folder and git directory are dropped.
func filterRepositories(body []byte, repository string) ([]byte, error) {
	if disabled(body) {
		return nil, errUnavailable
	}
	type clone struct {
		ID         string `json:"id"`
		Repository string `json:"repository"`
		Available  bool   `json:"available"`
	}
	type row struct {
		Repository string `json:"repository"`
		Settings   struct {
			Automatic *bool `json:"automatic"`
			Decisions *bool `json:"decisions"`
		} `json:"settings"`
		Clones       []clone  `json:"clones"`
		EarlierNames []string `json:"earlier_names,omitempty"`
	}
	var list []row
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	kept := []row{}
	for _, r := range list {
		if strings.EqualFold(r.Repository, repository) {
			kept = append(kept, r)
		}
	}
	return json.Marshal(kept)
}

// filterAutomatic keeps the status row of repository in an /api/automatic
// body (whether automatic reviews are on for it, the latest notice and how
// many wait), and drops the global switch and choice of pull requests, which
// are Settings', not the repository's.
func filterAutomatic(body []byte, repository string) ([]byte, error) {
	var v struct {
		Available    *bool          `json:"available"`
		Repositories []automaticRow `json:"repositories"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.Available != nil && !*v.Available {
		return nil, errUnavailable
	}
	kept := []automaticRow{}
	for _, r := range v.Repositories {
		if strings.EqualFold(r.Repository, repository) {
			kept = append(kept, r)
		}
	}
	return json.Marshal(map[string]any{"repositories": kept})
}

// filterLiveSessions keeps the sessions of repository in an
// /api/live-sessions body.
func filterLiveSessions(body []byte, repository string) ([]byte, error) {
	var v struct {
		Available *bool             `json:"available"`
		Sessions  []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.Available != nil && !*v.Available {
		return nil, errUnavailable
	}
	kept := []json.RawMessage{}
	for _, raw := range v.Sessions {
		if name, err := repositoryField(raw); err != nil {
			return nil, err
		} else if strings.EqualFold(name, repository) {
			kept = append(kept, raw)
		}
	}
	return json.Marshal(map[string]any{"sessions": kept})
}

// timelineRepositoryField reads the repository a Timeline detail names:
// {"repository":{"id","full_name"}}. A detail that names none (an archived
// feature) reads as no repository, which no action's matches.
func timelineRepositoryField(body []byte) (string, error) {
	var v struct {
		Repository *struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	if v.Repository == nil {
		return "", nil
	}
	return v.Repository.FullName, nil
}

// filterIndex keeps the entries of repository in an /api/index body; the
// problems, which name other clones, are dropped.
func filterIndex(body []byte, repository string) ([]byte, error) {
	var v struct {
		Available *bool             `json:"available"`
		Entries   []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.Available != nil && !*v.Available {
		return nil, errUnavailable
	}
	kept := []json.RawMessage{}
	for _, raw := range v.Entries {
		var e struct {
			PullRequest struct {
				Repository string `json:"repository"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		if strings.EqualFold(e.PullRequest.Repository, repository) {
			kept = append(kept, raw)
		}
	}
	return json.Marshal(map[string]any{"entries": kept, "problems": []string{}})
}

// filterInbox keeps the items of repository in an /api/inbox body, by their
// "owner/name#number" keys. The saved inboxes, which hold the user's own
// queries, and each item's saved-inbox ids are dropped, and the count is the
// daemon's own rule over what is kept: the items queued for the user, not
// being fixed, snoozed or archived (inboxqueue.Item.Queued and Hidden).
func filterInbox(body []byte, repository string) ([]byte, error) {
	var v struct {
		Available *bool             `json:"available"`
		Items     []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.Available != nil && !*v.Available {
		return nil, errUnavailable
	}
	kept := []map[string]json.RawMessage{}
	count := 0
	for _, raw := range v.Items {
		var item struct {
			Key      string `json:"key"`
			Kind     string `json:"kind"`
			Fixing   bool   `json:"fixing"`
			Snoozed  bool   `json:"snoozed"`
			Archived bool   `json:"archived"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		i := strings.LastIndex(item.Key, "#")
		if i < 0 || !strings.EqualFold(item.Key[:i], repository) {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		delete(fields, "inboxes")
		kept = append(kept, fields)
		if item.Kind != "" && item.Kind != "other" && !item.Fixing && !item.Snoozed && !item.Archived {
			count++
		}
	}
	return json.Marshal(map[string]any{"count": count, "items": kept, "inboxes": []string{}})
}

// filterGroups keeps the review groups of repository in an /api/groups body.
func filterGroups(body []byte, repository string) ([]byte, error) {
	var v struct {
		Available *bool             `json:"available"`
		Groups    []json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.Available != nil && !*v.Available {
		return nil, errUnavailable
	}
	kept := []json.RawMessage{}
	for _, raw := range v.Groups {
		if name, err := repositoryField(raw); err != nil {
			return nil, err
		} else if strings.EqualFold(name, repository) {
			kept = append(kept, raw)
		}
	}
	return json.Marshal(map[string]any{"available": true, "groups": kept})
}

// repositoryField reads a JSON object's top-level "repository".
func repositoryField(body []byte) (string, error) {
	var v struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	return v.Repository, nil
}

// Reader reads the daemon's local API in process, as the app does: the same
// routes, below the app server's authentication, with no credentials. It
// reads only through GET routes on its allowlist, without refresh flags; it
// has no way to send another method.
//
// A tool's reader reads only the route prefixes the tool declared
// (Tool.Reads), never the unreadable ones. An action's reader also reads only
// the routes in actionRoutes, each bound to the action's repository the way
// its handler finds what it serves, with no query parameter the entry does
// not list.
type Reader struct {
	api http.Handler
	// clone resolves a registered clone's id to its repository.
	clone func(ctx context.Context, id string) (string, error)
	allow []string
	// projected are the exact unreadable routes the tool may read (see
	// projectedReads); an action's reader never has any.
	projected  []string
	repository string
	// pull is the action's pull request, which a byKey route is bound to.
	pull int
}

// NewReader reads through api, the LocalAPI handler; clone resolves clone
// ids for an action's reader (nil refuses clone= for actions). It reads
// nothing until within gives it routes.
func NewReader(api http.Handler, clone func(ctx context.Context, id string) (string, error)) *Reader {
	return &Reader{api: api, clone: clone}
}

// within returns a reader of the given route prefixes, bound to repository
// when it is not empty.
func (r *Reader) within(allow []string, repository string) *Reader {
	return &Reader{api: r.api, clone: r.clone, allow: allow, repository: repository}
}

// forTool is the reader of one tool's call: its routes, its projected routes
// when the reader is no action's, and an action's repository and pull
// request.
func (r *Reader) forTool(t Tool, repository string, pull int) *Reader {
	reader := r.within(t.Reads, repository)
	reader.pull = pull
	if repository == "" {
		reader.projected = t.Projected
	}
	return reader
}

// Response is what one read returned.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

var (
	errReadTooLarge = errors.New("response too large")
	// errUnavailable is a route that answered, but not with what it holds:
	// the daemon has no such data now (a non-200 answer, or the capability's
	// {"available":false}).
	errUnavailable     = errors.New("unavailable")
	errNotReadable     = errors.New("not a route this tool reads")
	errRefreshFlag     = errors.New("reads never ask for a refresh")
	errNotActionRoute  = errors.New("not a route this agent reads")
	errOtherRepository = errors.New("this agent may read only its own pull request's repository")
	// errOtherPullRequest is a kept state (the person's draft review, their
	// walkthrough place) of a pull request that is not the action's own.
	errOtherPullRequest = errors.New("this agent may read only its own pull request's saved state")
)

// Get reads target, an /api/ path with an optional query.
func (r *Reader) Get(ctx context.Context, target string) (Response, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/api/") || path.Clean(u.Path) != u.Path {
		return Response{}, fmt.Errorf("read %q: not a local API path", target)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Response{}, fmt.Errorf("read %s: malformed query", u.Path)
	}
	for _, flag := range refreshFlags {
		if query.Has(flag) {
			return Response{}, fmt.Errorf("read %s: %w", u.Path, errRefreshFlag)
		}
	}
	if !readable(u.Path, r.allow) && !(r.repository == "" && slices.Contains(r.projected, u.Path)) {
		return Response{}, fmt.Errorf("read %s: %w", u.Path, errNotReadable)
	}
	// A pasted image is served as it was pasted, unmasked: no tool reads one.
	if strings.Contains(u.Path, "/transcript/images/") {
		return Response{}, fmt.Errorf("read %s: %w", u.Path, errNotReadable)
	}
	var route *actionRoute
	var vars map[string]string
	if r.repository != "" {
		if route, vars, err = r.bind(ctx, u.Path, query); err != nil {
			return Response{}, fmt.Errorf("read %s: %w", u.Path, err)
		}
	}
	if route != nil && route.bind == byLookup {
		// The record the id names must be the action's repository's before
		// anything it owns is read; a miss and another repository's record
		// look the same.
		record, err := r.fetch(ctx, route.lookup(vars), "")
		if err != nil {
			return Response{}, fmt.Errorf("read %s: %w", u.Path, err)
		}
		repository, err := route.check(record.Body)
		if record.Status != http.StatusOK || err != nil || !strings.EqualFold(repository, r.repository) {
			return Response{}, fmt.Errorf("read %s: %w", u.Path, errOtherRepository)
		}
	}
	response, err := r.fetch(ctx, u.Path, u.RawQuery)
	if err != nil {
		return Response{}, fmt.Errorf("read %s: %w", u.Path, err)
	}
	if route != nil {
		switch route.bind {
		case byResponse:
			// A miss and another repository's record look the same.
			repository, err := route.check(response.Body)
			if response.Status != http.StatusOK || err != nil || !strings.EqualFold(repository, r.repository) {
				return Response{}, fmt.Errorf("read %s: %w", u.Path, errOtherRepository)
			}
		case byFilter:
			if response.Status != http.StatusOK {
				return Response{}, fmt.Errorf("read %s: %w (status %d)", u.Path, errUnavailable, response.Status)
			}
			kept, err := route.filter(response.Body, r.repository)
			if err != nil {
				return Response{}, fmt.Errorf("read %s: %w", u.Path, err)
			}
			response.Body = kept
		}
	}
	return response, nil
}

// fetch serves one GET through the API.
func (r *Reader) fetch(ctx context.Context, p, rawQuery string) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, (&url.URL{Path: p, RawQuery: rawQuery}).String(), nil)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Accept", "application/json")
	limit := readLimit(p)
	w := &recorder{header: http.Header{}, limit: limit}
	r.api.ServeHTTP(w, req)
	if w.overflow {
		return Response{}, fmt.Errorf("%w (over %d bytes)", errReadTooLarge, limit)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return Response{Status: w.status, ContentType: w.header.Get("Content-Type"), Body: w.body.Bytes()}, nil
}

// readable reports whether p is under one of the allowed prefixes, a whole
// segment at a time, and under none of the unreadable ones.
func readable(p string, allow []string) bool {
	for _, prefix := range unreadable {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	for _, prefix := range allow {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// checkReads refuses route prefixes a tool may not declare.
func checkReads(reads []string) error {
	for _, prefix := range reads {
		if !strings.HasPrefix(prefix, "/api/") || path.Clean(prefix) != prefix {
			return fmt.Errorf("read prefix %q: not a local API path", prefix)
		}
		for _, hidden := range unreadable {
			if strings.HasPrefix(prefix, hidden) || strings.HasPrefix(hidden, prefix) {
				return fmt.Errorf("read prefix %q: %s is never readable", prefix, hidden)
			}
		}
	}
	return nil
}

// match reports whether p fits pattern, and the segments its wildcards took.
func match(pattern, p string) (map[string]string, bool) {
	want, got := strings.Split(pattern, "/"), strings.Split(p, "/")
	if len(want) != len(got) {
		return nil, false
	}
	vars := map[string]string{}
	for i, segment := range want {
		if name, ok := strings.CutPrefix(segment, "{"); ok && strings.HasSuffix(name, "}") {
			if got[i] == "" {
				return nil, false
			}
			vars[strings.TrimSuffix(name, "}")] = got[i]
		} else if segment != got[i] {
			return nil, false
		}
	}
	return vars, true
}

// bind finds an action's route for p and checks that its request names the
// action's repository and carries no parameter the route does not list. A
// byResponse route is checked again once it has answered.
func (r *Reader) bind(ctx context.Context, p string, query url.Values) (*actionRoute, map[string]string, error) {
	for i := range actionRoutes {
		route := &actionRoutes[i]
		vars, ok := match(route.pattern, p)
		if !ok {
			continue
		}
		allowed := map[string]bool{}
		for _, name := range route.params {
			allowed[name] = true
		}
		var named string
		switch route.bind {
		case byRepository:
			allowed["repository"] = true
			if len(query["repository"]) != 1 {
				return nil, nil, errOtherRepository
			}
			named = query.Get("repository")
		case byClone:
			allowed["clone"] = true
			if len(query["clone"]) != 1 || r.clone == nil {
				return nil, nil, errOtherRepository
			}
			repository, err := r.clone(ctx, query.Get("clone"))
			if err != nil {
				return nil, nil, errOtherRepository
			}
			named = repository
		case byPath:
			named = vars["owner"] + "/" + vars["name"]
		case byResponse, byLookup, byFilter:
			named = r.repository
		case byKey:
			allowed["key"] = true
			if len(query["key"]) != 1 || r.pull <= 0 || !route.key(query.Get("key"), r.repository, r.pull) {
				return nil, nil, errOtherPullRequest
			}
			named = r.repository
		}
		for name := range query {
			if !allowed[name] {
				return nil, nil, fmt.Errorf("%w: parameter %s", errNotActionRoute, name)
			}
		}
		if !strings.EqualFold(named, r.repository) {
			return nil, nil, errOtherRepository
		}
		return route, vars, nil
	}
	return nil, nil, errNotActionRoute
}

// recorder is the ResponseWriter a read hands the API: it keeps at most
// limit bytes and fails the read beyond that.
type recorder struct {
	limit    int
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *recorder) Header() http.Header { return w.header }
func (w *recorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *recorder) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if w.overflow || w.body.Len()+len(b) > w.limit {
		w.overflow = true
		return 0, errReadTooLarge
	}
	return w.body.Write(b)
}
