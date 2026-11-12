package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The tools of the Repositories page, its repository pages and the Report a
// problem dialog. None returns a filesystem path (a clone's folder is the
// person's home layout), a token, the environment or anything about signing
// in to GitHub: they return typed rows of what the page shows.

func (s *Server) repositoryTools() []Tool {
	return []Tool{{
		Name: "repositories",
		Description: "The repositories baum watches, as the Repositories page lists them: each one's automatic review choice (true or false, null when it follows the global switch), whether decision capture is on for it (false when turned off, null when it follows the global switch), the names GitHub forwards to it, and its local clones: how many, and for each whether it is available and its id. No folders. " +
			"Defaults to the open repository page's repository, else all.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","maxLength":200,"description":"Only this repository, as owner/name."}},"additionalProperties":false}`),
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/repositories"},
		Call:        callRepositories,
	}, {
		Name:        "clones_found",
		Description: "The local clones baum found on this computer by itself, as the Repositories page offers them: for each repository how many clones were found, their sources and when one was last active, the repositories whose clone was attached automatically, and when the search last ran. No folders.",
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/clones/found"},
		Call:        callClonesFound,
	}, {
		Name: "github_repositories",
		Description: "The repositories the person can pick on the Repositories page, as its picker lists them: owner/name, whether it is private or a fork, its description, when it was last pushed to, whether it is picked already and whether it has a local clone. " +
			"Pass query to search by name. Nothing about the GitHub connection itself.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","maxLength":200,"description":"Words to search the repositories' names for, as the picker's search box does."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":200,"description":"The most to return. Default 50."}},"additionalProperties":false}`),
		Scope:     ScopeGlobal,
		Projected: []string{"/api/github/repositories"},
		Call:      callGitHubRepositories,
	}, {
		Name:        "clone_target",
		Description: "The folder the Repositories page suggests cloning a repository into, written from the home folder as ~/...",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","maxLength":200,"description":"The repository as owner/name. Default: the open repository page's."}},"additionalProperties":false}`),
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/repositories"},
		Call:        callCloneTarget,
	}, {
		Name: "diagnostics_summary",
		Description: "What the app's Report a problem dialog checks and finds: each check's id, label, status (ok, warning, problem, info), version and where it was found, and the report's overall status. " +
			"No folders, details, fixes, environment or the issue text the dialog drafts.",
		Scope:     ScopeGlobal,
		Projected: []string{"/api/diagnostics"},
		Call:      callDiagnosticsSummary,
	}}
}

// pageRepository is the repository of the open repository page, "" otherwise.
func (c CallContext) pageRepository() string {
	if c.HasPage && c.Page.Page == PageRepositories {
		return c.Page.Repository
	}
	return ""
}

func callRepositories(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	only := a.Repository
	if only == "" {
		only = c.pageRepository()
	}
	body, _, err := c.get(ctx, "/api/repositories", nil)
	if err != nil {
		return "", err
	}
	if disabled(body) {
		return "", errors.New("repositories are unavailable")
	}
	var list []struct {
		Repository string `json:"repository"`
		Settings   struct {
			Automatic *bool `json:"automatic"`
			Decisions *bool `json:"decisions"`
		} `json:"settings"`
		Clones []struct {
			ID        string `json:"id"`
			Available bool   `json:"available"`
			Settings  struct {
				Automatic *bool `json:"automatic"`
				Decisions *bool `json:"decisions"`
			} `json:"settings"`
		} `json:"clones"`
		EarlierNames []string `json:"earlier_names"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", errors.New("the repositories are not readable")
	}
	type clone struct {
		ID        string `json:"id"`
		Available bool   `json:"available"`
	}
	type row struct {
		Repository   string   `json:"repository"`
		Automatic    *bool    `json:"automatic"`
		Decisions    *bool    `json:"decisions"`
		Clones       int      `json:"clone_count"`
		Available    int      `json:"available_clones"`
		CloneList    []clone  `json:"clones"`
		EarlierNames []string `json:"earlier_names,omitempty"`
	}
	rows := []row{}
	for _, r := range list {
		if only != "" && !strings.EqualFold(r.Repository, only) {
			continue
		}
		out := row{Repository: r.Repository, Automatic: r.Settings.Automatic, Decisions: r.Settings.Decisions, Clones: len(r.Clones), CloneList: []clone{}, EarlierNames: r.EarlierNames}
		for _, k := range r.Clones {
			out.CloneList = append(out.CloneList, clone{k.ID, k.Available})
			if k.Available {
				out.Available++
			}
		}
		rows = append(rows, out)
	}
	if only != "" && len(rows) == 0 {
		return "", errors.New(only + " is not a repository baum watches")
	}
	return compactJSON(map[string]any{"repositories": rows})
}

func callClonesFound(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	body, _, err := c.get(ctx, "/api/clones/found", nil)
	if err != nil {
		return "", err
	}
	if disabled(body) {
		return "", errors.New("clone search is unavailable: the daemon does not look for clones")
	}
	var found struct {
		ScannedAt    *time.Time `json:"scanned_at"`
		Running      bool       `json:"running"`
		Repositories map[string][]struct {
			LastActivity *time.Time `json:"last_activity"`
			Source       string     `json:"source"`
		} `json:"repositories"`
		Attached []struct {
			Repository string    `json:"repository"`
			At         time.Time `json:"at"`
		} `json:"attached"`
	}
	if err := json.Unmarshal(body, &found); err != nil {
		return "", errors.New("the found clones are not readable")
	}
	type repository struct {
		Repository   string     `json:"repository"`
		Clones       int        `json:"clones"`
		Sources      []string   `json:"sources"`
		LastActivity *time.Time `json:"last_activity,omitempty"`
	}
	type attached struct {
		Repository string    `json:"repository"`
		At         time.Time `json:"at"`
	}
	out := struct {
		ScannedAt    *time.Time   `json:"scanned_at"`
		Running      bool         `json:"running"`
		Repositories []repository `json:"repositories"`
		Attached     []attached   `json:"attached"`
	}{ScannedAt: found.ScannedAt, Running: found.Running, Repositories: []repository{}, Attached: []attached{}}
	names := make([]string, 0, len(found.Repositories))
	for name := range found.Repositories {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		r := repository{Repository: name, Sources: []string{}}
		for _, k := range found.Repositories[name] {
			r.Clones++
			if k.Source != "" && !slices.Contains(r.Sources, k.Source) {
				r.Sources = append(r.Sources, k.Source)
			}
			if k.LastActivity != nil && (r.LastActivity == nil || k.LastActivity.After(*r.LastActivity)) {
				r.LastActivity = k.LastActivity
			}
		}
		out.Repositories = append(out.Repositories, r)
	}
	for _, a := range found.Attached {
		out.Attached = append(out.Attached, attached{a.Repository, a.At})
	}
	return compactJSON(out)
}

func callGitHubRepositories(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// Whatever went wrong, the answer says only that the list is
	// unavailable: why is about the GitHub connection, which is not shared.
	unavailable := errors.New("the repository list is unavailable now: the app's picker shows why")
	body, _, err := c.get(ctx, "/api/github/repositories", url.Values{"query": {a.Query}})
	if err != nil || disabled(body) {
		return "", unavailable
	}
	var listing struct {
		TotalCount   int `json:"total_count"`
		Repositories []struct {
			Repository  string     `json:"repository"`
			Private     bool       `json:"private"`
			Fork        bool       `json:"fork"`
			PushedAt    *time.Time `json:"pushed_at"`
			Description string     `json:"description"`
			Picked      bool       `json:"picked"`
			Clone       string     `json:"clone"`
		} `json:"repositories"`
		Searching bool `json:"searching"`
		HasMore   bool `json:"has_more"`
	}
	if json.Unmarshal(body, &listing) != nil {
		return "", unavailable
	}
	type item struct {
		Repository  string     `json:"repository"`
		Private     bool       `json:"private"`
		Fork        bool       `json:"fork,omitempty"`
		Description string     `json:"description,omitempty"`
		PushedAt    *time.Time `json:"pushed_at,omitempty"`
		Picked      bool       `json:"picked"`
		Cloned      bool       `json:"cloned"`
	}
	limit := boundRows(a.Limit, defaultRows, maxRows)
	items := []item{}
	for _, r := range listing.Repositories {
		if len(items) == limit {
			break
		}
		items = append(items, item{r.Repository, r.Private, r.Fork, r.Description, r.PushedAt, r.Picked, r.Clone != ""})
	}
	return compactJSON(struct {
		Repositories []item `json:"repositories"`
		Total        int    `json:"total"`
		Searching    bool   `json:"searching,omitempty"`
		More         bool   `json:"more,omitempty"`
	}{items, max(listing.TotalCount, len(listing.Repositories)), listing.Searching, listing.HasMore || len(listing.Repositories) > len(items)})
}

// homeDirectory is the home folder a path is written relative to.
var homeDirectory = os.UserHomeDir

// tildePath is path with the home folder written ~, as the app and the
// transcript write it; a path outside it is only its last folder's name.
func tildePath(path string) string {
	if home, err := homeDirectory(); err == nil && filepath.IsAbs(home) {
		for _, h := range []string{home, realPath(home)} {
			if rel, err := filepath.Rel(h, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				if rel == "." {
					return "~"
				}
				return "~/" + filepath.ToSlash(rel)
			}
		}
	}
	return ".../" + filepath.Base(path)
}

func realPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

func callCloneTarget(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository := a.Repository
	if repository == "" {
		repository = c.pageRepository()
	}
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || segment("owner", owner) != nil || segment("name", name) != nil || strings.Contains(name, "/") {
		return "", errors.New("name the repository as owner/name")
	}
	body, missing, err := c.get(ctx, "/api/repositories/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/clone-target", nil)
	if err != nil {
		return "", errors.New("no folder could be suggested for " + repository)
	}
	var target struct {
		Dir string `json:"dir"`
	}
	if missing || disabled(body) || json.Unmarshal(body, &target) != nil || target.Dir == "" {
		return "", errors.New("no folder could be suggested for " + repository)
	}
	return compactJSON(map[string]string{"repository": repository, "folder": tildePath(target.Dir)})
}

// diagnosticCheck is what the summary keeps of a doctor check. The check's
// detail, folder and fix, the home folder, and the issue the dialog drafts
// are never read out.
type diagnosticCheck struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
}

func callDiagnosticsSummary(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	body, _, err := c.get(ctx, "/api/diagnostics", nil)
	if err != nil {
		return "", errors.New("diagnostics are unavailable now")
	}
	var answer struct {
		Home   string `json:"home"`
		Report *struct {
			Status string `json:"status"`
			Checks []struct {
				ID      string `json:"id"`
				Label   string `json:"label"`
				Status  string `json:"status"`
				Version string `json:"version"`
				Source  string `json:"source"`
			} `json:"checks"`
		} `json:"report"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return "", errors.New("the diagnostics are not readable")
	}
	if answer.Report == nil {
		return "", errors.New("the daemon has run no diagnostics")
	}
	// A label, version or source is words, but one could quote a folder: the
	// home folder is written ~ in them, as the app writes it.
	home := func(text string) string {
		if answer.Home != "" {
			text = strings.ReplaceAll(text, answer.Home, "~")
		}
		return text
	}
	out := struct {
		Status string            `json:"status"`
		Checks []diagnosticCheck `json:"checks"`
	}{Status: answer.Report.Status, Checks: []diagnosticCheck{}}
	for _, k := range answer.Report.Checks {
		out.Checks = append(out.Checks, diagnosticCheck{k.ID, home(k.Label), k.Status, home(k.Version), home(k.Source)})
	}
	return compactJSON(out)
}
