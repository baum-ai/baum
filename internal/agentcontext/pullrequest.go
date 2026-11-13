package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// maxPatchFiles and patchBudget bound the patches one diff call returns: at
// most this many files and about this many bytes of patch text in all. The
// result lists the files left out by path.
const (
	maxPatchFiles = 100
	patchBudget   = 1 << 20
)

// pullTools are the tools of the pull request page.
func (s *Server) pullTools() []Tool {
	index := "/api/index"
	return []Tool{{
		Name:        "pull_request",
		Description: "The pull request as the app's page shows it: title, author, state, draft, branches, head commit, merge state, checks and review decision, size and URL; its saved review's status; the description; where it stands in the user's inbox (what it waits for, being fixed, snoozed or archived), and the review groups and stacks it belongs to.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/prs/description", "/api/inbox", "/api/groups"},
		Call:        callPullRequest,
	}, {
		Name:        "diff",
		Description: "The pull request's changed files (path, change, lines added and deleted, hunks) and their patches: the patch of the file named by path, or of every file up to a limit. Read from the saved review when there is one, else from the facts the app shows before a review.",
		InputSchema: targetSchema(`"path":{"type":"string","description":"A changed file's path. Omit for every file's patch."}`),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/facts", "/api/document", "/api/patch"},
		Call:        callDiff,
	}, {
		Name:        "commits",
		Description: "The pull request's commits, oldest first, with the local agent sessions linked to each, as the app's page shows them.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/facts", "/api/commits"},
		Call:        callCommits,
	}, {
		Name:        "checks",
		Description: "The checks of the pull request's head commit, whatever their state, and the failing ones with what they report.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/prs/checks", "/api/prs/failing-checks"},
		Call:        callChecks,
	}, {
		Name:        "review_threads",
		Description: "The pull request's unresolved review threads with their comments.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/prs/review-threads"},
		Call:        callReviewThreads,
	}, {
		Name:        "merge_state",
		Description: "What the app offers to do about the pull request's state: the merge methods and whether it can merge, whether the branch is behind or conflicting and can be updated, the files a conflict is in, and the failed workflow runs that can be re-run. Nothing is changed.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/prs/merge-options", "/api/prs/update-branch-options", "/api/prs/rerun-checks-options", "/api/prs/conflicts"},
		Call:        callMergeState,
	}, {
		Name:        "jobs",
		Description: "The background jobs of the pull request (reviews, fixes, chats), oldest first: state, phase, why one failed, a fix's summary and pushed commit, and whether it kept a transcript.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/jobs"},
		Call:        callJobs,
	}, {
		Name:        "job_transcript",
		Description: "A fix job's transcript of its agent's run, from entry after on. Only fix jobs keep one. The job must belong to the pull request.",
		InputSchema: targetSchema(`"id":{"type":"string","description":"The job's id, from jobs."},"after":{"type":"integer","minimum":0,"description":"Read the entries after this sequence number; a result's next."}`, "id"),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/jobs"},
		Call:        callJobTranscript,
	}, {
		Name:        "sessions",
		Description: "The local agent sessions linked to the pull request through its commits: agent, title, branch, times, outcome and edited files.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/facts", "/api/sessions"},
		Call:        callSessions,
	}, {
		Name:        "session_transcript",
		Description: "The redacted transcript of one session linked to the pull request, as the app shows it.",
		InputSchema: targetSchema(`"id":{"type":"string","description":"The session's id, from sessions."}`, "id"),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/facts", "/api/sessions"},
		Call:        callSessionTranscript,
	}, {
		Name:        "focus",
		Description: "The part of the open page the user picked: a section, a stop, the overview and the anchors, as the page describes it. \"none\" when nothing is picked.",
		Scope:       ScopeRepository,
		Call: func(_ context.Context, c CallContext, _ json.RawMessage) (string, error) {
			if !c.HasPage || len(c.Page.Focus) == 0 || string(c.Page.Focus) == "null" {
				return "none", nil
			}
			return compactJSON(struct {
				Page  string          `json:"page"`
				Focus json.RawMessage `json:"focus"`
			}{c.Page.Page, c.Page.Focus})
		},
	}}
}

func callPullRequest(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	out := map[string]any{"repository": repository, "number": number}
	out["pull_request"] = e.raw["pull_request"]
	review := map[string]any{}
	for key, value := range e.raw {
		// The clone is the daemon's own handle, not something the page shows.
		if key != "pull_request" && key != "clone_id" && key != "latest" {
			review[key] = value
		}
	}
	if latest := projectLatest(e.raw["latest"]); latest != nil {
		review["latest"] = latest
	}
	out["review"] = review
	body, _, err := c.get(ctx, "/api/prs/description", pullQuery(repository, number))
	if err != nil {
		out["description_error"] = err.Error()
	} else if body != nil {
		out["description"] = body
	}
	out["inbox"] = c.inboxState(ctx, repository, number)
	groups, err := c.groupsOf(ctx, repository, number)
	if err != nil {
		out["groups_error"] = err.Error()
	} else {
		out["groups"] = groups
	}
	return compactJSON(out)
}

// latestFields are what the page shows of a pull request's latest saved
// review: its version and heads, who generated it and when, and what it cost.
// Never the session that wrote it, the accounting, digests or policy.
var latestFields = []string{"version", "head", "base", "merge_base", "backend", "model", "generated_at", "recorded_cost_usd"}

func projectLatest(raw json.RawMessage) map[string]json.RawMessage {
	var all map[string]json.RawMessage
	if json.Unmarshal(raw, &all) != nil || all == nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	for _, field := range latestFields {
		if v, ok := all[field]; ok {
			out[field] = v
		}
	}
	return out
}

// groupRef is a review group or stack a pull request belongs to.
type groupRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stack bool   `json:"stack"`
}

// groupsOf lists the repository's groups that have the pull request as a
// member, as the page's group rail does.
func (c CallContext) groupsOf(ctx context.Context, repository string, number int) ([]groupRef, error) {
	body, _, err := c.get(ctx, "/api/groups", nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Available *bool `json:"available"`
		Groups    []struct {
			groupRef
			Repository string `json:"repository"`
			Members    []int  `json:"members"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errors.New("the groups are not readable")
	}
	if list.Available != nil && !*list.Available {
		return nil, errors.New("review groups are unavailable")
	}
	out := []groupRef{}
	for _, g := range list.Groups {
		if strings.EqualFold(g.Repository, repository) && slices.Contains(g.Members, number) {
			out = append(out, g.groupRef)
		}
	}
	return out, nil
}

// inboxState is what the daemon's inbox says of the pull request: what it
// waits for (kind), being fixed, snoozed, archived; or why it says nothing.
func (c CallContext) inboxState(ctx context.Context, repository string, number int) any {
	body, _, err := c.get(ctx, "/api/inbox", nil)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	var inbox struct {
		Items []struct {
			Key      string `json:"key"`
			Kind     string `json:"kind"`
			Fixing   bool   `json:"fixing"`
			Snoozed  bool   `json:"snoozed"`
			Archived bool   `json:"archived"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &inbox) != nil {
		return nil
	}
	key := strings.ToLower(repository) + "#" + strconv.Itoa(number)
	for _, item := range inbox.Items {
		if strings.EqualFold(item.Key, key) {
			return map[string]any{"kind": item.Kind, "fixing": item.Fixing, "snoozed": item.Snoozed, "archived": item.Archived}
		}
	}
	return nil
}

// fileChange is one changed file as the diff tool lists it.
type fileChange struct {
	Path      string          `json:"path"`
	OldPath   string          `json:"old_path,omitempty"`
	Change    string          `json:"change"`
	Additions int             `json:"additions"`
	Deletions int             `json:"deletions"`
	Hunks     int             `json:"hunks"`
	Patch     json.RawMessage `json:"patch"`
}

func changedFiles(hunks []sourceHunk) []fileChange {
	var files []fileChange
	at := map[string]int{}
	for _, h := range hunks {
		i, ok := at[h.Path]
		if !ok {
			i = len(files)
			at[h.Path] = i
			files = append(files, fileChange{Path: h.Path, OldPath: h.OldPath, Change: h.Change})
		}
		files[i].Additions += h.Additions
		files[i].Deletions += h.Deletions
		files[i].Hunks++
	}
	return files
}

func callDiff(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
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
	source, err := c.sourceOf(ctx, e, true)
	if err != nil {
		return "", err
	}
	files := changedFiles(source.Hunks)
	want := files
	if a.Path != "" {
		want = nil
		for _, f := range files {
			if f.Path == a.Path {
				want = []fileChange{f}
			}
		}
		if want == nil {
			return "", fmt.Errorf("%s is not a changed file of the pull request", a.Path)
		}
	}
	var omitted []string
	total := 0
	for i := range want {
		if i >= maxPatchFiles || total >= patchBudget {
			omitted = append(omitted, want[i].Path)
			continue
		}
		route, query := source.route("patch")
		query.Set("path", want[i].Path)
		body, missing, err := c.get(ctx, route, query)
		if err != nil {
			return "", err
		}
		if missing {
			continue
		}
		var patch struct {
			Patch json.RawMessage `json:"patch"`
		}
		if json.Unmarshal(body, &patch) == nil && string(patch.Patch) != "null" {
			want[i].Patch = patch.Patch
			total += len(patch.Patch)
		}
	}
	out := map[string]any{"repository": repository, "number": number, "source": source.Name, "head": source.Head, "files": want}
	if len(omitted) > 0 {
		out["patches_omitted"] = map[string]any{
			"reason": fmt.Sprintf("the patches stop at %d files or %d bytes: ask for these by path", maxPatchFiles, patchBudget),
			"files":  omitted,
		}
	}
	return compactJSON(out)
}

// readSource reads one route of the pull request's source (commits,
// sessions) as the page does, for the call's pull request.
func readSource(ctx context.Context, c CallContext, args json.RawMessage, name string) (json.RawMessage, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return nil, err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return nil, err
	}
	source, err := c.sourceOf(ctx, e, false)
	if err != nil {
		return nil, err
	}
	route, query := source.route(name)
	body, missing, err := c.get(ctx, route, query)
	if err != nil {
		return nil, err
	}
	if missing && source.unbuilt {
		// The daemon has no facts at this head yet: ask for them, which builds
		// them, and read again at the head they describe.
		if source, err = c.sourceOf(ctx, e, true); err != nil {
			return nil, err
		}
		route, query = source.route(name)
		if body, missing, err = c.get(ctx, route, query); err != nil {
			return nil, err
		}
	}
	if missing {
		return nil, fmt.Errorf("the pull request's %s are not available from its %s", name, source.Name)
	}
	return body, nil
}

func callCommits(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	body, err := readSource(ctx, c, args, "commits")
	return string(body), err
}

func callSessions(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	body, err := readSource(ctx, c, args, "sessions")
	return string(body), err
}

// segment reports an id fit to be one path segment.
func segment(kind, id string) error {
	if id == "" || strings.ContainsAny(id, "/\\?#") || id == "." || id == ".." {
		return fmt.Errorf("%s is not a %s id", id, kind)
	}
	return nil
}

func callSessionTranscript(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if err := segment("session", a.ID); err != nil {
		return "", err
	}
	// The viewer answers only for a session the pull request's commits link.
	body, err := readSource(ctx, c, args, "sessions/"+url.PathEscape(a.ID))
	return string(body), err
}

func callChecks(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	query := pullQuery(repository, number)
	query.Set("head_sha", e.HeadSHA)
	out := map[string]any{"repository": repository, "number": number, "head_sha": e.HeadSHA}
	// As the page does: every check, and the failing ones alone only from a
	// daemon that has no all-checks route (not found).
	body, missing, err := c.get(ctx, "/api/prs/checks", query)
	if err != nil {
		return "", err
	}
	if missing {
		if body, missing, err = c.get(ctx, "/api/prs/failing-checks", query); err != nil || missing {
			return "", errors.New("the pull request's checks are not available")
		}
		out["only_failing"] = true
	}
	var list struct {
		Checks []json.RawMessage `json:"checks"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", errors.New("the checks are not readable")
	}
	out["checks"] = list.Checks
	failing := []json.RawMessage{}
	for _, raw := range list.Checks {
		var check struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		}
		if json.Unmarshal(raw, &check) == nil && (check.Status == "" || check.Status == "completed") && failingConclusions[check.Conclusion] {
			failing = append(failing, raw)
		}
	}
	out["failing"] = failing
	return compactJSON(out)
}

// failingConclusions are the conclusions the page counts as failing.
var failingConclusions = map[string]bool{"failure": true, "timed_out": true, "startup_failure": true, "action_required": true, "error": true}

func callReviewThreads(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	query := pullQuery(repository, number)
	// The app names the unresolved count discovery has, so a cached list that
	// no longer matches is read again.
	if e, err := c.entry(ctx, repository, number); err == nil && e.Unresolved != nil {
		query.Set("unresolved", strconv.Itoa(*e.Unresolved))
	}
	body, _, err := c.get(ctx, "/api/prs/review-threads", query)
	return string(body), err
}

func callMergeState(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	out := map[string]any{"repository": repository, "number": number}
	var conflict bool
	for _, part := range []struct{ key, path string }{
		{"merge", "/api/prs/merge-options"},
		{"update", "/api/prs/update-branch-options"},
		{"rerun_checks", "/api/prs/rerun-checks-options"},
	} {
		body, _, err := c.get(ctx, part.path, pullQuery(repository, number))
		if err != nil {
			out[part.key+"_error"] = err.Error()
			continue
		}
		out[part.key] = body
		if part.key == "update" {
			var options struct {
				Conflict bool `json:"conflict"`
			}
			_ = json.Unmarshal(body, &options)
			conflict = options.Conflict
		}
	}
	// As the page does, the files of a conflict are read only for a pull
	// request that conflicts.
	if conflict {
		body, _, err := c.get(ctx, "/api/prs/conflicts", pullQuery(repository, number))
		if err != nil {
			out["conflicts_error"] = err.Error()
		} else {
			out["conflicts"] = body
		}
	}
	return compactJSON(out)
}

// jobFields are what the jobs tool keeps of a job: its state and outcome,
// never its accounting or configuration.
var jobFields = []string{"id", "kind", "state", "phase", "reason", "detail", "summary", "commit", "commit_url", "has_transcript", "head_sha", "backend", "model", "only", "automatic", "trigger", "thread_outcomes", "created_at", "started_at", "updated_at"}

func callJobs(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	body, _, err := c.get(ctx, "/api/jobs", pullQuery(repository, number))
	if err != nil {
		return "", err
	}
	var answer struct {
		Available *bool             `json:"available"`
		Jobs      []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Jobs == nil {
		// Jobs are unavailable: the route says so.
		return string(body), nil
	}
	jobs := make([]map[string]json.RawMessage, 0, len(answer.Jobs))
	for _, raw := range answer.Jobs {
		var all map[string]json.RawMessage
		if json.Unmarshal(raw, &all) != nil {
			continue
		}
		job := map[string]json.RawMessage{}
		for _, field := range jobFields {
			if v, ok := all[field]; ok {
				job[field] = v
			}
		}
		jobs = append(jobs, job)
	}
	return compactJSON(map[string]any{"repository": repository, "number": number, "jobs": jobs})
}

func callJobTranscript(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		ID    string `json:"id"`
		After *int   `json:"after"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if err := segment("job", a.ID); err != nil {
		return "", err
	}
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	// The job must be the repository's and the pull request's before its
	// transcript is read.
	record, missing, err := c.get(ctx, "/api/jobs/"+url.PathEscape(a.ID), nil)
	if err != nil {
		return "", err
	}
	var job struct {
		Repository    string `json:"repository"`
		PR            int    `json:"pr"`
		HasTranscript bool   `json:"has_transcript"`
	}
	if missing || json.Unmarshal(record, &job) != nil || !strings.EqualFold(job.Repository, repository) || job.PR != number {
		return "", fmt.Errorf("job %s is not a job of pull request %d", a.ID, number)
	}
	if !job.HasTranscript {
		return "This job kept no transcript: only fix jobs do.", nil
	}
	query := url.Values{}
	if a.After != nil {
		query.Set("after", strconv.Itoa(*a.After))
	}
	body, missing, err := c.get(ctx, "/api/jobs/"+url.PathEscape(a.ID)+"/transcript", query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("this job's transcript is no longer available")
	}
	return string(body), nil
}
