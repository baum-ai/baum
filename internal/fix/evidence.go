package fix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/baum-ai/baum/internal/github"
)

// Reader is the GitHub REST access a fix needs; *github.RESTReader is one.
type Reader interface {
	Read(ctx context.Context, path, etag string) (int, http.Header, []byte, error)
	Tail(ctx context.Context, path string, limit int) (int, []byte, error)
}

// PullRequest is what GitHub reports of a pull request a fix is for.
type PullRequest struct {
	// Title is the pull request's, untrusted.
	Title   string
	State   string
	Draft   bool
	HeadSHA string
	// HeadRef is the head branch; HeadRepository and BaseRepository are
	// "owner/name", and differ for a fork.
	HeadRef, HeadRepository string
	BaseRef, BaseRepository string
	// Mergeable is nil while GitHub has not computed it.
	Mergeable      *bool
	MergeableState string
	URL            string
}

// Conflicts reports whether GitHub says the pull request cannot be merged
// cleanly into its base.
func (p PullRequest) Conflicts() bool {
	return (p.Mergeable != nil && !*p.Mergeable) || p.MergeableState == "dirty"
}

// Fork reports a head branch in another repository than the base, which a
// fix never pushes to.
func (p PullRequest) Fork() bool {
	return p.HeadRepository == "" || !strings.EqualFold(p.HeadRepository, p.BaseRepository)
}

func repositoryPath(repository github.Repository) string {
	return "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
}

func readJSON(ctx context.Context, r Reader, path string, v any) error {
	status, _, raw, err := r.Read(ctx, path, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GitHub answered HTTP %d", status)
	}
	return json.Unmarshal(raw, v)
}

// ReadPullRequest reads an open pull request.
func ReadPullRequest(ctx context.Context, r Reader, repository github.Repository, number int) (PullRequest, error) {
	var raw struct {
		Title, State   string
		Draft          bool
		Mergeable      *bool
		MergeableState string `json:"mergeable_state"`
		HTML           string `json:"html_url"`
		Head, Base     struct {
			SHA, Ref string
			Repo     *struct {
				FullName string `json:"full_name"`
			}
		}
	}
	if err := readJSON(ctx, r, fmt.Sprintf("%s/pulls/%d", repositoryPath(repository), number), &raw); err != nil {
		return PullRequest{}, errors.New("pull request evidence unavailable")
	}
	if raw.State != "open" || raw.Head.SHA == "" || raw.Head.Ref == "" || raw.Base.Ref == "" || raw.Base.Repo == nil {
		return PullRequest{}, errors.New("open pull request evidence required")
	}
	p := PullRequest{Title: bounded(raw.Title, 512), State: raw.State, Draft: raw.Draft, HeadSHA: raw.Head.SHA, HeadRef: raw.Head.Ref, BaseRef: raw.Base.Ref, BaseRepository: raw.Base.Repo.FullName, Mergeable: raw.Mergeable, MergeableState: raw.MergeableState, URL: raw.HTML}
	if raw.Head.Repo != nil {
		p.HeadRepository = raw.Head.Repo.FullName
	}
	return p, nil
}

// CanPush reports whether the signed-in account may push to the repository:
// GitHub's permissions.push, which write, maintain and admin access give.
// Who wrote the pull request does not matter; whoever confirms the fix pushes.
func CanPush(ctx context.Context, r Reader, repository github.Repository) (bool, error) {
	var raw struct {
		Permissions *struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := readJSON(ctx, r, repositoryPath(repository), &raw); err != nil {
		return false, err
	}
	return raw.Permissions != nil && raw.Permissions.Push, nil
}

// Check is a failing check of a pull request's head commit. Everything but
// Name and Conclusion comes from the check's own output and is untrusted.
type Check struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	// Title, Summary and Text are a check run's output; a commit status has
	// its description in Summary.
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Text    string `json:"text,omitempty"`
	// Log is the end of a GitHub Actions job's log, read by Logs.
	Log string `json:"log,omitempty"`
	// JobID is the GitHub Actions job behind a check run, 0 for another app.
	JobID int64 `json:"-"`
}

// Bounds on what a check contributes to a prompt.
const (
	maxCheckField = 4 << 10
	maxChecks     = 20
	MaxLogChecks  = 4
	MaxLogLines   = 200
	maxLogBytes   = 32 << 10
)

// failing are the check-run conclusions and commit-status states that make
// the checks fail.
var failing = map[string]bool{"failure": true, "timed_out": true, "startup_failure": true, "action_required": true, "error": true}

// FailingChecks lists the head commit's failing check runs (their latest
// attempt) and commit statuses, at most 20, with their output bounded.
func FailingChecks(ctx context.Context, r Reader, repository github.Repository, sha string) ([]Check, error) {
	var runs struct {
		CheckRuns []struct {
			ID         int64
			Name       string
			Conclusion string
			App        *struct{ Slug string }
			Output     struct{ Title, Summary, Text string }
		} `json:"check_runs"`
	}
	path := repositoryPath(repository) + "/commits/" + url.PathEscape(sha)
	if err := readJSON(ctx, r, path+"/check-runs?per_page=100&filter=latest", &runs); err != nil {
		return nil, fmt.Errorf("reading the checks: %w", err)
	}
	var checks []Check
	for _, run := range runs.CheckRuns {
		if !failing[run.Conclusion] {
			continue
		}
		check := Check{Name: bounded(run.Name, 256), Conclusion: run.Conclusion, Title: bounded(run.Output.Title, 512), Summary: bounded(run.Output.Summary, maxCheckField), Text: bounded(run.Output.Text, maxCheckField)}
		if run.App != nil && run.App.Slug == "github-actions" {
			check.JobID = run.ID
		}
		checks = append(checks, check)
	}
	var combined struct {
		Statuses []struct {
			Context, State, Description string
		}
	}
	if err := readJSON(ctx, r, path+"/status?per_page=100", &combined); err != nil {
		return nil, fmt.Errorf("reading the commit statuses: %w", err)
	}
	for _, status := range combined.Statuses {
		if failing[status.State] {
			checks = append(checks, Check{Name: bounded(status.Context, 256), Conclusion: status.State, Summary: bounded(status.Description, maxCheckField)})
		}
	}
	return checks[:min(len(checks), maxChecks)], nil
}

// Logs reads the end of the job log of the first four failing GitHub
// Actions checks: their last 200 lines, at most 32 KiB each. A log that
// cannot be read is left out.
func Logs(ctx context.Context, r Reader, repository github.Repository, checks []Check) []Check {
	out := append([]Check(nil), checks...)
	read := 0
	for i := range out {
		if out[i].JobID == 0 || read == MaxLogChecks {
			continue
		}
		read++
		status, body, err := r.Tail(ctx, fmt.Sprintf("%s/actions/jobs/%d/logs", repositoryPath(repository), out[i].JobID), maxLogBytes)
		if err != nil || status != http.StatusOK {
			continue
		}
		out[i].Log = lastLines(string(body), MaxLogLines)
	}
	return out
}

func lastLines(text string, n int) string {
	text = strings.ToValidUTF8(strings.TrimRight(text, "\n"), "")
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func bounded(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "…"
}
