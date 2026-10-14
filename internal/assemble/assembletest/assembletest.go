// Package assembletest builds the synthetic inputs of bundle assembly tests:
// a git clone whose pull request exercises every kind of change, a fake
// GitHub that returns evidence for it, and a fake session source. Everything
// is made up and deterministic: commit dates and identities are fixed, so
// the commit ids, and the bundle built from them, are the same on every run.
package assembletest

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baumai/baum/bundle"
	"github.com/baumai/baum/internal/github"
	"github.com/baumai/baum/internal/gitlocal"
)

// PlantedSecret is a made-up Hugging Face token, planted in the pull request's
// settings file, body, a comment and a session prompt. It must never reach a
// bundle. It is kept in pieces so no source line reads as a credential.
var PlantedSecret = "hf_" + "nW2OPJ0rGsD6y4OUtEXQxmAa3BItWMoP6J"

// PlantedEmail is planted next to the secret.
const PlantedEmail = "ada.lovelace@example.com"

// PNG is a 1x1 PNG, the description image's content.
var PNG = mustDecode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

func mustDecode(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Repository is the pull request's repository on GitHub.
var Repository = github.Repository{Host: "github.com", Owner: "acme", Name: "demo"}

// Number is the pull request's number.
const Number = 7

// Options vary the fixture.
type Options struct {
	// NoReviewMD leaves REVIEW.md out of the base commit.
	NoReviewMD bool
}

// Repo is a clone with a pull request on branch "feature". The clone's HEAD
// is on main, as when a user reads a pull request they are not working on.
type Repo struct {
	Dir string
	// Base is main's tip, Head the pull request's tip, MergeBase their merge
	// base, which is the commit before the pull request's work: main moved on
	// after the branch point, so the base is not the merge base.
	Base, Head, MergeBase string
	// Commits are the pull request's commits, oldest first.
	Commits []string
	// ShiftHead and ShiftBase are a second pull request making the same
	// change to src/shift.go as the first, against a base that has three more
	// lines above it: a context shift.
	ShiftHead, ShiftBase string
}

// env is the environment every git process of the fixture runs in.
func env(date string) []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
}

// Git runs git in dir and returns its trimmed output.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return git(t, dir, "2026-07-01T10:00:00Z", args...)
}

func git(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env(date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write writes a file of the clone.
func Write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, date, message string) string {
	t.Helper()
	git(t, dir, date, "add", "-A")
	git(t, dir, date, "commit", "-q", "-m", message)
	return git(t, dir, date, "rev-parse", "HEAD")
}

func numbered(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s line %d\n", prefix, i)
	}
	return b.String()
}

// listSource has two functions far enough apart for git to cut two hunks,
// which the pull request changes with the same added line: repeated hunks.
func listSource(annotated bool) string {
	note := ""
	if annotated {
		note = "\t// note: keep in sync with the schema\n"
	}
	var b strings.Builder
	b.WriteString("package demo\n\nfunc First() int {\n" + note + "\treturn 1\n}\n\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "// filler %d\n", i)
	}
	b.WriteString("\nfunc Second() int {\n" + note + "\treturn 2\n}\n")
	return b.String()
}

func shiftSource(changed bool, preamble string) string {
	line := "const Limit = 10\n"
	if changed {
		line = "const Limit = 25\n"
	}
	return "package demo\n\n" + preamble + "// Limit bounds the retries.\n" + line + "\n" + numbered("// trailing", 8)
}

// NewRepo creates the fixture clone in a temporary directory. The pull
// request, on branch feature, in two commits:
//
//   - adds the same line twice to src/list.go (repeated hunks), adds
//     config/settings.env holding PlantedSecret, edits src/shift.go, and adds
//     a docs/ page;
//   - renames docs/old-guide.md to docs/guide.md with one line changed (a
//     rename), and changes assets/logo.bin (a binary change).
//
// Main then gains a commit, so the base commit is not the merge base.
func NewRepo(t *testing.T, opts Options) Repo {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "2026-06-01T09:00:00Z", "init", "-q", "-b", "main")
	Write(t, dir, "README.md", "# demo\n")
	if !opts.NoReviewMD {
		Write(t, dir, "REVIEW.md", "Prefer small functions and name the invariant a change relies on.\n")
	}
	Write(t, dir, "src/list.go", listSource(false))
	Write(t, dir, "src/shift.go", shiftSource(false, ""))
	Write(t, dir, "docs/old-guide.md", numbered("guide", 12))
	Write(t, dir, "assets/logo.bin", "logo-v1\x00\x01\x02\x03")
	mergeBase := commit(t, dir, "2026-06-01T09:00:00Z", "Start the demo")

	// The shift pair: main's next commit adds three lines above the change.
	git(t, dir, "2026-06-02T09:00:00Z", "checkout", "-q", "-b", "shift-base")
	Write(t, dir, "src/shift.go", shiftSource(false, "// Added by an unrelated change.\n// Two more lines.\n// Three.\n"))
	shiftBase := commit(t, dir, "2026-06-02T09:00:00Z", "Document the retries")
	git(t, dir, "2026-06-02T09:30:00Z", "checkout", "-q", "-b", "shift-feature")
	Write(t, dir, "src/shift.go", shiftSource(true, "// Added by an unrelated change.\n// Two more lines.\n// Three.\n"))
	shiftHead := commit(t, dir, "2026-06-02T10:00:00Z", "Raise the limit")

	git(t, dir, "2026-06-03T09:00:00Z", "checkout", "-q", "-b", "feature", mergeBase)
	Write(t, dir, "src/list.go", listSource(true))
	Write(t, dir, "config/settings.env", "HF_TOKEN="+PlantedSecret+"\nMODE=test\n")
	Write(t, dir, "src/shift.go", shiftSource(true, ""))
	Write(t, dir, "docs/new.md", "# New\n\nA page.\n")
	first := commit(t, dir, "2026-06-03T10:00:00Z", "Add notes and settings")
	git(t, dir, "2026-06-03T10:30:00Z", "mv", "docs/old-guide.md", "docs/guide.md")
	Write(t, dir, "docs/guide.md", strings.Replace(numbered("guide", 12), "guide line 6\n", "guide line six\n", 1))
	Write(t, dir, "assets/logo.bin", "logo-v2\x00\x04\x05\x06\x07")
	second := commit(t, dir, "2026-06-03T11:00:00Z", "Rename the guide and refresh the logo")

	git(t, dir, "2026-06-04T09:00:00Z", "checkout", "-q", "main")
	Write(t, dir, "README.md", "# demo\n\nMain moved on.\n")
	base := commit(t, dir, "2026-06-04T09:00:00Z", "Update the readme")
	return Repo{
		Dir: dir, Base: base, Head: second, MergeBase: mergeBase, Commits: []string{first, second},
		ShiftBase: shiftBase, ShiftHead: shiftHead,
	}
}

// Dirty leaves the clone with a modified tracked file, a staged change and
// an untracked file, so a test can tell whether anything touched them.
func (r Repo) Dirty(t *testing.T) {
	t.Helper()
	Write(t, r.Dir, "README.md", "# demo\n\nLocal edit, not committed.\n")
	Write(t, r.Dir, "staged.txt", "staged\n")
	Git(t, r.Dir, "add", "staged.txt")
	Write(t, r.Dir, "untracked.txt", "untracked\n")
}

// State describes what a read must leave alone: the status, HEAD, every ref
// and the index's staged paths.
func (r Repo) State(t *testing.T) string {
	t.Helper()
	return strings.Join([]string{
		Git(t, r.Dir, "status", "--porcelain=v1", "--untracked-files=all"),
		Git(t, r.Dir, "rev-parse", "HEAD"),
		Git(t, r.Dir, "for-each-ref"),
		Git(t, r.Dir, "ls-files", "--stage"),
	}, "\n--\n")
}

var at = time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)

func stamp(minutes int) *time.Time {
	t := at.Add(time.Duration(minutes) * time.Minute)
	return &t
}

// description is the pull request's body: a heading, an image, an issue
// reference, and the planted secret and e-mail address.
func description() string {
	return "## Summary\n\nKeep the notes in sync. Follow-up to #9, tracked as DEMO-12.\n\n## Screenshot\n\n![Diagram](" + ImageSource + ")\n\n" +
		"Test token: " + PlantedSecret + " (ask " + PlantedEmail + ")\n"
}

// ImageSource is the description image's markdown source, and ImageFetchURL
// the signed URL GitHub renders for it.
const (
	ImageSource   = "https://github.com/user-attachments/assets/0f3b8c1e-5a4d-4e6f-9a7b-1c2d3e4f5a6b"
	ImageFetchURL = "https://private-user-images.githubusercontent.com/1/2-0f3b8c1e-5a4d-4e6f-9a7b-1c2d3e4f5a6b.png?jwt=signed"
)

// Evidence is what GitHub returns for the pull request at r's head: every
// part present, with a check run, a status, a conversation, a thread and a
// description image.
func (r Repo) Evidence() github.Evidence {
	required, optional := true, false
	pr := bundle.PullRequest{
		Number: Number, State: "open", Title: "Keep the notes in sync", Body: description(),
		AuthorLogin: "ada", AuthorAssociation: "MEMBER", BaseRef: "main", BaseSHA: r.Base,
		HeadRef: "feature", HeadSHA: r.Head, RequestedReviewers: []string{"grace"}, OpenedAt: stamp(60),
	}
	for i, sha := range r.Commits {
		subject := []string{"Add notes and settings", "Rename the guide and refresh the logo"}[i]
		parent := r.MergeBase
		if i > 0 {
			parent = r.Commits[i-1]
		}
		pr.Commits = append(pr.Commits, bundle.Commit{SHA: sha, Subject: subject, CommittedAt: stamp(60 + 90*i), Parents: []string{parent}})
	}
	line := 3
	return github.Evidence{
		Host: "github.com", Repository: "acme/demo", PullRequest: pr,
		Checks: []bundle.Check{
			{ID: bundle.CheckRunID(101), Name: "build", Kind: "check_run", Status: "completed", Conclusion: "success", Required: &required, URL: "https://github.com/acme/demo/runs/101", CompletedAt: stamp(200)},
			{ID: bundle.StatusCheckID("SC_1", 0), Name: "ci/lint", Kind: "status", Status: "success", Conclusion: "success", Required: &optional},
		},
		Conversation: []bundle.ConversationItem{
			{ID: "1001", Kind: "comment", AuthorLogin: "grace", AuthorAssociation: "MEMBER", Body: "Did the settings file ship a real token? " + PlantedSecret + " Contact " + PlantedEmail, CreatedAt: stamp(220)},
			{ID: "1002", Kind: "review", AuthorLogin: "grace", AuthorAssociation: "MEMBER", Body: "Please drop the token.", State: "changes_requested", CreatedAt: stamp(230), Commit: r.Head},
			{ID: "1003", Kind: "review_comment", AuthorLogin: "grace", AuthorAssociation: "MEMBER", Body: "Remove this.", Path: "config/settings.env", CreatedAt: stamp(231), Review: "1002", Thread: "PRRT_1", ThreadRoot: true},
		},
		Threads: []bundle.Thread{{ID: "PRRT_1", Path: "config/settings.env", Line: &line, URL: "https://github.com/acme/demo/pull/7#discussion_r1003"}},
		Images: []bundle.RenderedImage{{
			ID: bundle.AttachmentID(ImageSource), SourceURL: ImageSource, Alt: "Diagram of " + PlantedEmail, FetchURL: ImageFetchURL,
		}},
		Sources: bundle.Sources{
			github.SourcePullRequest: true, github.SourceRequestedReviewers: true, github.SourceCommits: true,
			github.SourceComments: true, github.SourceReviews: true, github.SourceReviewThreads: true,
			github.SourceChecks: true, github.SourceRequiredChecks: true,
		},
	}
}

// EvidenceMissing is the pull request with nothing but its header: no
// checks, conversation, threads or images, and every other part unreadable.
func (r Repo) EvidenceMissing() github.Evidence {
	evidence := r.Evidence()
	evidence.PullRequest.Body = "Keep the notes in sync."
	evidence.PullRequest.RequestedReviewers = nil
	evidence.Checks, evidence.Conversation, evidence.Images = nil, nil, nil
	evidence.Threads = []bundle.Thread{}
	evidence.Sources = bundle.Sources{github.SourcePullRequest: true}
	evidence.Unavailable = map[string]error{
		github.SourceChecks:        fmt.Errorf("resource not accessible by personal access token"),
		github.SourceReviewThreads: fmt.Errorf("resource not accessible by personal access token"),
	}
	for _, part := range []string{github.SourceRequestedReviewers, github.SourceCommits, github.SourceComments, github.SourceReviews, github.SourceRequiredChecks} {
		evidence.Sources[part] = false
	}
	evidence.Sources[github.SourceChecks] = false
	evidence.Sources[github.SourceReviewThreads] = false
	return evidence
}

// ShiftEvidence is the header of the context-shift pull request: the same
// change to src/shift.go as the main pull request's, against a base with
// three more lines above it.
func (r Repo) ShiftEvidence() github.Evidence {
	evidence := r.EvidenceMissing()
	evidence.PullRequest.Body = ""
	evidence.PullRequest.Commits = nil
	evidence.Unavailable = nil
	evidence.PullRequest.HeadSHA, evidence.PullRequest.BaseSHA = r.ShiftHead, r.ShiftBase
	return evidence
}

// GitHub is a fake GitHub: it returns Evidence, or Err, for the pull request.
type GitHub struct {
	Evidence github.Evidence
	Err      error
	// Fetched records each pull request asked for.
	Fetched []github.Target
}

// Fetch implements assemble.GitHub.
func (g *GitHub) Fetch(_ context.Context, repo github.Repository, number int, _ github.FetchOptions) (github.Evidence, error) {
	g.Fetched = append(g.Fetched, github.Target{Repository: repo, Number: number})
	return g.Evidence, g.Err
}

// Sessions is a fake session source returning Found, or Err, whatever the
// commits, and recording the commits it was asked for.
type Sessions struct {
	Found   []bundle.Session
	Err     error
	Commits []gitlocal.Commit
}

// Evidence implements assemble.Sessions.
func (s *Sessions) Evidence(_ context.Context, commits []gitlocal.Commit, _, _ int) ([]bundle.Session, error) {
	s.Commits = commits
	return s.Found, s.Err
}

// SessionTree is what the local session source returns for a top-level
// session whose second prompt spawned a subagent that spawned another: each
// prompt carries the paths the sessions it started, at any depth, touched. A
// second, earlier-starting session ran in the same pull request. Both carry
// the planted secret and e-mail address in their text.
func SessionTree() []bundle.Session {
	return []bundle.Session{
		{
			RootSessionID: "claude-session-1", Vendor: "claude_code", Models: []string{"claude-opus-5"},
			Title: "Keep the notes in sync", Branch: "feature", StartedAt: stamp(70), EndedAt: stamp(140), Outcome: "committed",
			Prompts: []bundle.Prompt{
				{At: stamp(70), Index: 1, ID: "p-1", Text: "Add the note to both functions in src/list.go", Reply: "Done.", TouchedPaths: []string{"src/list.go"}},
				{At: stamp(100), Index: 2, ID: "p-2", Text: "Delegate the settings and docs. The token is " + PlantedSecret + ", mail " + PlantedEmail, Reply: "The subagent finished.",
					// Touched by the prompt itself, the subagent it spawned and the
					// subagent that one spawned.
					TouchedPaths: []string{"config/settings.env", "docs/guide.md", "docs/new.md"}},
			},
			CommandEvents: []bundle.CommandEvent{
				{ID: "t-1", Time: stamp(110), Outcome: "succeeded", Commands: []bundle.CommandEntry{{Program: "go", Category: "test", Target: "src/..."}}},
			},
			Confidence: 0.95, HasStartedAt: true, StartedAtValue: *stamp(70),
		},
		{
			RootSessionID: "codex-session-2", Vendor: "codex", Models: []string{"gpt-5"},
			Title: "Refresh the logo", Branch: "feature", StartedAt: stamp(30), EndedAt: stamp(60), Outcome: "uncommitted",
			Prompts:    []bundle.Prompt{{At: stamp(30), Index: 1, ID: "p-1", Text: "Replace assets/logo.bin", TouchedPaths: []string{"assets/logo.bin"}}},
			Confidence: 0.6, HasStartedAt: true, StartedAtValue: *stamp(30),
		},
	}
}
