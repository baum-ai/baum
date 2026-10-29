// Package prupdate makes the GitHub writes that unblock a pull request
// without changing its code: merging its base branch into it (update branch),
// re-running its failed GitHub Actions workflow runs, and resolving a review
// thread. Like merging, the first two are confirmed by the user against the
// head commit they saw, and each is re-checked against GitHub before anything
// is sent.
package prupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/github"
)

// Failure codes, stable for the app to branch on.
const (
	HeadMoved = "head_moved"
	Conflict  = "conflict"
	Blocked   = "blocked"
	Forbidden = "forbidden"
	NotOpen   = "not_open"
	UpToDate  = "up_to_date"
	NoRuns    = "no_runs"
	Fork      = "fork"
	NotFound  = "not_found"
	Failed    = "failed"
)

// Error is a refusal, worded for the person who clicked the button.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

const (
	headMovedMessage = "The pull request changed since you opened it. Review the new commits and try again."
	unreachable      = "baum could not reach GitHub. Check your connection and try again."
	reconnect        = "GitHub rejected the saved connection. Reconnect GitHub in Settings and try again."
	rateLimited      = "GitHub is limiting requests right now. Try again in a minute."
	conflictMessage  = "This pull request has conflicts with its base branch. Resolve them before updating the branch."
)

// Service reads and writes through one GitHub client. Cache, when set,
// keeps the review threads and failing checks it read for the Inbox panel.
// Counted, when set, hears of a pull request's unresolved threads read from
// GitHub that no longer number discovery's count, so the inbox shows the new
// count before discovery's next poll.
type Service struct {
	Client  *github.Client
	Cache   *Cache
	Counted func(ctx context.Context, repository github.Repository, number, unresolved int)
}

// session is one repository's REST access, with the pull request read once.
type session struct {
	reader *github.RESTReader
	pull   fix.PullRequest
}

func (s Service) open(ctx context.Context, repository github.Repository, number int) (session, error) {
	reader, err := s.Client.REST(ctx, repository)
	if err != nil {
		return session{}, &Error{Failed, unreachable}
	}
	pull, err := fix.ReadPullRequest(ctx, reader, repository, number)
	if err != nil {
		return session{}, &Error{NotOpen, "GitHub could not show this pull request as open. It may be closed."}
	}
	return session{reader, pull}, nil
}

func repositoryPath(repository github.Repository) string {
	return "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
}

// canPush refuses early, and in plain words, an account that cannot write.
func canPush(ctx context.Context, r fix.Reader, repository github.Repository) error {
	ok, err := fix.CanPush(ctx, r, repository)
	switch {
	case err != nil:
		return &Error{Failed, "baum could not read your access to this repository on GitHub."}
	case !ok:
		return &Error{Forbidden, "Your GitHub account cannot push to this repository."}
	}
	return nil
}

// UpdateOptions is what updating the branch would do, read just before the
// user confirms.
type UpdateOptions struct {
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	HeadSHA string `json:"head_sha"`
	HeadRef string `json:"head_ref"`
	BaseRef string `json:"base_ref"`
	// Behind is GitHub's report that the head is out of date with its base.
	Behind   bool `json:"behind"`
	Conflict bool `json:"conflict"`
	Fork     bool `json:"fork"`
}

// UpdateOptions reads the pull request's branches and whether it is behind or
// conflicting.
func (s Service) UpdateOptions(ctx context.Context, repository github.Repository, number int) (UpdateOptions, error) {
	read, err := s.open(ctx, repository, number)
	if err != nil {
		return UpdateOptions{}, err
	}
	p := read.pull
	return UpdateOptions{State: "OPEN", Draft: p.Draft, HeadSHA: p.HeadSHA, HeadRef: p.HeadRef, BaseRef: p.BaseRef, Behind: p.MergeableState == "behind", Conflict: p.Conflicts(), Fork: p.Fork()}, nil
}

// UpdateBranch merges the base branch into the pull request's head branch, as
// GitHub's Update branch button does. GitHub refuses when the head is no
// longer expectedHead, so the user updates only the commit they confirmed. The
// update itself runs on GitHub after it accepts the request.
func (s Service) UpdateBranch(ctx context.Context, repository github.Repository, number int, expectedHead string) error {
	read, err := s.open(ctx, repository, number)
	if err != nil {
		return err
	}
	switch {
	case read.pull.HeadSHA != expectedHead:
		return &Error{HeadMoved, headMovedMessage}
	case read.pull.Fork():
		return &Error{Fork, "This pull request's branch is in a fork, which baum does not update."}
	case read.pull.Conflicts():
		return &Error{Conflict, conflictMessage}
	}
	if err := canPush(ctx, read.reader, repository); err != nil {
		return err
	}
	path := fmt.Sprintf("%s/pulls/%d/update-branch", repositoryPath(repository), number)
	status, body, err := read.reader.Write(ctx, http.MethodPut, path, map[string]string{"expected_head_sha": expectedHead})
	if err != nil {
		return &Error{Failed, unreachable}
	}
	if status == http.StatusAccepted || status == http.StatusOK {
		return nil
	}
	return updateError(status, message(body))
}

// updateError words GitHub's refusal of an update. Its messages are for
// developers, so only the recognized causes are rephrased; others are quoted.
func updateError(status int, text string) error {
	lower := strings.ToLower(text)
	switch {
	case status == http.StatusUnauthorized:
		return &Error{Forbidden, reconnect}
	case strings.Contains(lower, "expected_head_sha") || strings.Contains(lower, "expected head sha"):
		return &Error{HeadMoved, headMovedMessage}
	case strings.Contains(lower, "conflict"):
		return &Error{Conflict, conflictMessage}
	case strings.Contains(lower, "no new commits") || strings.Contains(lower, "up to date"):
		return &Error{UpToDate, "This branch is already up to date with its base branch."}
	case strings.Contains(lower, "protect") || strings.Contains(lower, "required status") || strings.Contains(lower, "signed commits") || strings.Contains(lower, "ruleset"):
		return &Error{Blocked, "The branch's protection rules do not allow GitHub to update it: " + text}
	case status == http.StatusForbidden && strings.Contains(lower, "rate limit"):
		return &Error{Failed, rateLimited}
	case status == http.StatusForbidden || status == http.StatusNotFound:
		return &Error{Forbidden, "Your GitHub account is not allowed to update this pull request's branch."}
	case text == "":
		return &Error{Failed, fmt.Sprintf("GitHub refused the update (HTTP %d).", status)}
	}
	return &Error{Failed, "GitHub refused the update: " + text}
}

// message is the "message" of a GitHub error body, or empty.
func message(body []byte) string {
	var reply struct{ Message string }
	_ = json.Unmarshal(body, &reply)
	return strings.TrimSpace(reply.Message)
}

// Run is a GitHub Actions workflow run of the head commit.
type Run struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RerunOptions lists what re-running the failed checks would do.
type RerunOptions struct {
	State   string `json:"state"`
	HeadSHA string `json:"head_sha"`
	// Runs are the head commit's finished workflow runs that failed, were
	// cancelled or timed out; each is re-run for its failed jobs only.
	Runs []Run `json:"runs"`
	// NotRerunnable names the failing checks no workflow run is behind: other
	// apps' checks and commit statuses, which only their own app can re-run.
	NotRerunnable []string `json:"not_rerunnable"`
}

// rerunnable are the conclusions of a finished run worth re-running.
var rerunnable = map[string]bool{"failure": true, "cancelled": true, "timed_out": true}

// RerunOptions finds the failed workflow runs of the pull request's head.
func (s Service) RerunOptions(ctx context.Context, repository github.Repository, number int) (RerunOptions, error) {
	read, err := s.open(ctx, repository, number)
	if err != nil {
		return RerunOptions{}, err
	}
	return rerunOptions(ctx, repository, read)
}

func rerunOptions(ctx context.Context, repository github.Repository, read session) (RerunOptions, error) {
	sha := read.pull.HeadSHA
	out := RerunOptions{State: "OPEN", HeadSHA: sha, Runs: []Run{}, NotRerunnable: []string{}}
	var list struct {
		WorkflowRuns []struct {
			ID                       int64 `json:"id"`
			Name, Status, Conclusion string
		} `json:"workflow_runs"`
	}
	status, _, body, err := read.reader.Read(ctx, fmt.Sprintf("%s/actions/runs?head_sha=%s&per_page=100", repositoryPath(repository), url.QueryEscape(sha)), "")
	if err != nil || status != http.StatusOK || json.Unmarshal(body, &list) != nil {
		return RerunOptions{}, &Error{Failed, "baum could not read the workflow runs from GitHub."}
	}
	for _, run := range list.WorkflowRuns {
		if run.Status == "completed" && rerunnable[run.Conclusion] {
			out.Runs = append(out.Runs, Run{ID: run.ID, Name: run.Name})
		}
	}
	checks, err := fix.FailingChecks(ctx, read.reader, repository, sha)
	if err != nil {
		return RerunOptions{}, &Error{Failed, "baum could not read the failing checks from GitHub."}
	}
	for _, check := range checks {
		if check.JobID == 0 {
			out.NotRerunnable = append(out.NotRerunnable, check.Name)
		}
	}
	return out, nil
}

// Refusal is one workflow run GitHub would not re-run.
type Refusal struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// RerunResult is what a re-run started.
type RerunResult struct {
	Rerun   []Run     `json:"rerun"`
	Refused []Refusal `json:"refused"`
}

// RerunFailedChecks re-runs the failed jobs of each failed workflow run of the
// head commit the user confirmed. It errors only when no run was started; runs
// GitHub refuses beside started ones are reported in the result. The run
// list is read here, never taken from the request.
func (s Service) RerunFailedChecks(ctx context.Context, repository github.Repository, number int, expectedHead string) (RerunResult, error) {
	read, err := s.open(ctx, repository, number)
	if err != nil {
		return RerunResult{}, err
	}
	if read.pull.HeadSHA != expectedHead {
		return RerunResult{}, &Error{HeadMoved, headMovedMessage}
	}
	if err := canPush(ctx, read.reader, repository); err != nil {
		return RerunResult{}, err
	}
	options, err := rerunOptions(ctx, repository, read)
	if err != nil {
		return RerunResult{}, err
	}
	if len(options.Runs) == 0 {
		return RerunResult{}, &Error{NoRuns, "No failed GitHub Actions workflow run was found for this commit, so there is nothing baum can re-run."}
	}
	result := RerunResult{Rerun: []Run{}, Refused: []Refusal{}}
	var first error
	for _, run := range options.Runs {
		path := fmt.Sprintf("%s/actions/runs/%d/rerun-failed-jobs", repositoryPath(repository), run.ID)
		status, body, err := read.reader.Write(ctx, http.MethodPost, path, nil)
		if err == nil && status == http.StatusCreated {
			result.Rerun = append(result.Rerun, run)
			continue
		}
		refusal := rerunError(status, message(body))
		if err != nil {
			refusal = &Error{Failed, unreachable}
		}
		if first == nil {
			first = refusal
		}
		result.Refused = append(result.Refused, Refusal{Name: run.Name, Message: refusal.Error()})
	}
	if len(result.Rerun) == 0 {
		return result, first
	}
	s.Cache.rerun(headKey(repository, expectedHead))
	return result, nil
}

// rerunError words GitHub's refusal to re-run a workflow run.
func rerunError(status int, text string) *Error {
	lower := strings.ToLower(text)
	switch {
	case status == http.StatusUnauthorized:
		return &Error{Forbidden, reconnect}
	case status == http.StatusForbidden && strings.Contains(lower, "rate limit"):
		return &Error{Failed, rateLimited}
	case status == http.StatusForbidden && (strings.Contains(lower, "too old") || strings.Contains(lower, "retention") || strings.Contains(lower, "expired")):
		return &Error{Failed, "The workflow run is too old for GitHub to re-run."}
	case status == http.StatusForbidden || status == http.StatusNotFound:
		return &Error{Forbidden, "Your GitHub account is not allowed to re-run workflows in this repository."}
	case status == http.StatusConflict || strings.Contains(lower, "in progress") || strings.Contains(lower, "already running") || strings.Contains(lower, "still running"):
		return &Error{Failed, "The workflow run is still running."}
	case text == "":
		return &Error{Failed, fmt.Sprintf("GitHub refused the re-run (HTTP %d).", status)}
	}
	return &Error{Failed, "GitHub refused the re-run: " + text}
}

// Thread is an unresolved review thread as the app lists it. Excerpt is the
// headline of its first comment, the reviewer's text, untrusted.
type Thread struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Outdated bool   `json:"outdated"`
	Author   string `json:"author"`
	Excerpt  string `json:"excerpt"`
	Comments int    `json:"comments"`
	URL      string `json:"url"`
	// CanResolve is the signed-in account's permission to resolve it.
	CanResolve bool `json:"can_resolve"`
}

// Threads lists a pull request's unresolved review threads.
type Threads struct {
	Threads []Thread `json:"threads"`
	// Capped: the pull request has more threads than baum read.
	Capped bool `json:"capped"`
}

// maxExcerpt bounds a thread's excerpt, in characters.
const maxExcerpt = 200

// ReviewThreads returns the pull request's unresolved review threads, from
// the cache while it still matches unresolved, discovery's count (negative
// when unknown), else read from GitHub.
func (s Service) ReviewThreads(ctx context.Context, repository github.Repository, number, unresolved int) (Threads, error) {
	key, gen := pullKey(repository, number), s.Cache.generation()
	if cached, ok := s.Cache.cachedThreads(key, unresolved); ok {
		return cached, nil
	}
	list, err := s.Client.UnresolvedThreads(ctx, repository, number)
	if err != nil {
		return Threads{}, &Error{Failed, "baum could not read the review threads from GitHub."}
	}
	out := Threads{Threads: []Thread{}, Capped: list.Capped}
	for _, t := range list.Unresolved {
		first := t.Comments[0]
		out.Threads = append(out.Threads, Thread{ID: t.ID, Path: t.Path, Line: t.Line, Outdated: t.Outdated, Author: first.Author, Excerpt: excerpt(first.Body), Comments: max(t.Total, len(t.Comments)), URL: t.URL, CanResolve: t.CanResolve})
	}
	s.Cache.storeThreads(key, gen, out)
	if s.Counted != nil && unresolved >= 0 && !out.Capped && len(out.Threads) != unresolved {
		s.Counted(ctx, repository, number, len(out.Threads))
	}
	return out, nil
}

// excerpt is a comment's headline (fix.Headline), at most maxExcerpt
// characters.
func excerpt(body string) string {
	line := fix.Headline(body)
	if utf8.RuneCountInString(line) > maxExcerpt {
		line = strings.TrimSpace(string([]rune(line)[:maxExcerpt-1])) + "…"
	}
	return line
}

// ResolveThread resolves one review thread of the pull request, as GitHub's
// Resolve conversation button does. The thread is read first, so only a thread
// of this pull request is resolved; one someone resolved meanwhile is done.
func (s Service) ResolveThread(ctx context.Context, repository github.Repository, number int, id string) error {
	state, err := s.Client.ThreadState(ctx, repository, id)
	switch {
	case err != nil:
		return &Error{Failed, "baum could not read the review thread from GitHub."}
	case !strings.EqualFold(state.Repository, repository.Owner+"/"+repository.Name) || state.Number != number:
		return &Error{NotFound, "This review thread is not on this pull request."}
	case state.Resolved:
		s.Cache.resolved(pullKey(repository, number), id)
		return nil
	case !state.CanResolve:
		return &Error{Forbidden, "Your GitHub account cannot resolve this review thread."}
	}
	if err := s.Client.ResolveThread(ctx, repository, id); err != nil {
		return &Error{Failed, "GitHub did not resolve the review thread. Try again, or resolve it on GitHub."}
	}
	s.Cache.resolved(pullKey(repository, number), id)
	return nil
}

// FailingCheck is a failing check of the head commit as the app lists it.
// Name and Title come from the check itself and are untrusted.
type FailingCheck struct {
	Name string `json:"name"`
	// Conclusion is the check run's conclusion or the commit status's state:
	// failure, timed_out, startup_failure, action_required or error.
	Conclusion string `json:"conclusion"`
	// Title is the check's own one-line account of the failure, if any.
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	// JobID is the GitHub Actions job behind a check run, which RerunJob
	// re-runs; absent for other apps' checks and commit statuses.
	JobID int64 `json:"job_id,omitempty"`
}

// Checks lists a head commit's failing checks.
type Checks struct {
	HeadSHA string         `json:"head_sha"`
	Checks  []FailingCheck `json:"checks"`
}

// maxCheckTitle bounds a check's title, in characters.
const maxCheckTitle = 160

// FailingChecks lists the failing check runs and commit statuses of head,
// the pull request's head commit as the app saw it, or of the pull
// request's current head when head is empty.
func (s Service) FailingChecks(ctx context.Context, repository github.Repository, number int, head string) (Checks, error) {
	gen := s.Cache.generation()
	if head != "" {
		if cached, ok := s.Cache.cachedChecks(headKey(repository, head)); ok {
			return cached, nil
		}
	}
	reader, err := s.Client.REST(ctx, repository)
	if err != nil {
		return Checks{}, &Error{Failed, unreachable}
	}
	if head == "" {
		pull, err := fix.ReadPullRequest(ctx, reader, repository, number)
		if err != nil {
			return Checks{}, &Error{NotOpen, "GitHub could not show this pull request as open. It may be closed."}
		}
		head = pull.HeadSHA
	}
	list, err := fix.FailingChecks(ctx, reader, repository, head)
	if err != nil {
		return Checks{}, &Error{Failed, "baum could not read the failing checks from GitHub."}
	}
	out := Checks{HeadSHA: head, Checks: []FailingCheck{}}
	for _, check := range list {
		out.Checks = append(out.Checks, FailingCheck{Name: check.Name, Conclusion: check.Conclusion, Title: checkTitle(check), URL: check.URL, JobID: check.JobID})
	}
	s.Cache.storeChecks(headKey(repository, head), gen, out)
	return out, nil
}

// RerunJob re-runs one failed GitHub Actions job of the head commit the user
// saw, as GitHub's Re-run job button does. The job must be one of that
// head's failing checks, read here, so a request cannot name another job.
func (s Service) RerunJob(ctx context.Context, repository github.Repository, number int, expectedHead string, job int64) error {
	read, err := s.open(ctx, repository, number)
	if err != nil {
		return err
	}
	if read.pull.HeadSHA != expectedHead {
		return &Error{HeadMoved, headMovedMessage}
	}
	if err := canPush(ctx, read.reader, repository); err != nil {
		return err
	}
	checks, err := fix.FailingChecks(ctx, read.reader, repository, expectedHead)
	if err != nil {
		return &Error{Failed, "baum could not read the failing checks from GitHub."}
	}
	if job == 0 || !slices.ContainsFunc(checks, func(c fix.Check) bool { return c.JobID == job }) {
		return &Error{NotFound, "This check is no longer failing on this commit, or GitHub Actions did not run it."}
	}
	path := fmt.Sprintf("%s/actions/jobs/%d/rerun", repositoryPath(repository), job)
	status, body, err := read.reader.Write(ctx, http.MethodPost, path, nil)
	if err != nil {
		return &Error{Failed, unreachable}
	}
	if status != http.StatusCreated {
		return rerunError(status, message(body))
	}
	s.Cache.rerun(headKey(repository, expectedHead))
	return nil
}

// checkTitle is a check's output title, else the headline of its summary (a
// commit status's description), at most maxCheckTitle characters.
func checkTitle(check fix.Check) string {
	title := fix.Headline(check.Title)
	if title == "" {
		title = fix.Headline(check.Summary)
	}
	if utf8.RuneCountInString(title) > maxCheckTitle {
		title = strings.TrimSpace(string([]rune(title)[:maxCheckTitle-1])) + "…"
	}
	return title
}

// AsError returns err as a refusal, wording any other error as a connection
// failure.
func AsError(err error) *Error {
	var refusal *Error
	if errors.As(err, &refusal) {
		return refusal
	}
	return &Error{Failed, unreachable}
}
