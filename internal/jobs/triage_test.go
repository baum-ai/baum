package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

const (
	reviewedHead = "1111111111111111111111111111111111111111"
	newHead      = "2222222222222222222222222222222222222222"
)

const savedReview = `{"summary":{"title":"Add retries","text":"Jobs back off and stop."},"sections":[{"title":"Retry policy","summary":{"text":"Adds RetryPolicy."},"risks":[{"tier":"high","text":"Retries in step","state":"active"},{"tier":"low","text":"Gone","state":"mitigated"}]}]}`

type fakeVersions struct {
	head string
	err  error
}

func (f fakeVersions) Latest(context.Context, store.Key) (store.Version, error) {
	if f.err != nil {
		return store.Version{}, f.err
	}
	return store.Version{Metadata: store.Metadata{Head: f.head}, Document: []byte(savedReview)}, nil
}

type fakeDiffs struct {
	diff    string
	err     error
	fetched bool
}

func (f *fakeDiffs) FetchPulls(context.Context, string, []gitlocal.PullRequest) error {
	f.fetched = true
	return nil
}

func (f *fakeDiffs) Diff(_ context.Context, base, head string, _ int64) ([]byte, error) {
	if base != reviewedHead || head != newHead {
		return nil, errors.New("unexpected range " + base + ".." + head)
	}
	return []byte(f.diff), f.err
}

type completerBackend struct {
	reply string
	err   error
	asked agent.CompleteRequest
	calls int
}

func (c *completerBackend) Name() string { return "claude" }
func (c *completerBackend) Run(context.Context, agent.Request) (agent.Result, error) {
	return agent.Result{}, errors.New("triage must not run an agent")
}
func (c *completerBackend) Complete(_ context.Context, r agent.CompleteRequest) (agent.Completion, error) {
	c.calls++
	c.asked = r
	return agent.Completion{Document: []byte(c.reply)}, c.err
}

// runBackend is a backend without single calls (agent.Completer).
type runBackend struct{}

func (runBackend) Name() string { return "cursor-agent" }
func (runBackend) Run(context.Context, agent.Request) (agent.Result, error) {
	return agent.Result{}, nil
}

func TestTriage(t *testing.T) {
	onChange := repos.Preferences{Automatic: true, Updates: repos.UpdatesOnChange}
	for name, tc := range map[string]struct {
		prefs    repos.Preferences
		versions fakeVersions
		diff     *fakeDiffs
		backend  agent.Backend
		// unchanged is the reason triage stood the review down with, ""
		// when the review is written.
		unchanged string
		asked     bool
	}{
		"insignificant": {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "diff --git a/README.md b/README.md\n-teh\n+the\n"}, &completerBackend{reply: `{"regenerate":false,"reason":"Only fixes a typo in the README."}`}, "Only fixes a typo in the README.", true},
		"significant":   {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "diff --git a/x.go b/x.go\n"}, &completerBackend{reply: `{"regenerate":true,"reason":"Changes the retry loop."}`}, "", true},
		"empty diff":    {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "\n"}, &completerBackend{}, "The new commits change no files since the review.", false},
		"always":        {repos.Preferences{Automatic: true}, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "x"}, &completerBackend{reply: `{"regenerate":false,"reason":"r"}`}, "", false},
		"on open":       {repos.Preferences{Automatic: true, Updates: repos.UpdatesOnOpen}, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "x"}, &completerBackend{reply: `{"regenerate":false,"reason":"r"}`}, "", false},
		"no review":     {onChange, fakeVersions{err: store.ErrNotFound}, &fakeDiffs{diff: "x"}, &completerBackend{reply: `{"regenerate":false,"reason":"r"}`}, "", false},
		"same head":     {onChange, fakeVersions{head: newHead}, &fakeDiffs{diff: "x"}, &completerBackend{reply: `{"regenerate":false,"reason":"r"}`}, "", false},
		"diff too big":  {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{err: errors.New("output too large")}, &completerBackend{reply: `{"regenerate":false,"reason":"r"}`}, "", false},
		"call fails":    {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "x"}, &completerBackend{err: errors.New("provider down")}, "", true},
		"bad reply":     {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "x"}, &completerBackend{reply: `not json`}, "", true},
		"no completer":  {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "x"}, runBackend{}, "", false},
		"empty reason":  {onChange, fakeVersions{head: reviewedHead}, &fakeDiffs{diff: "x"}, &completerBackend{reply: `{"regenerate":false,"reason":"  "}`}, "The new commits don't change what the review says.", true},
	} {
		t.Run(name, func(t *testing.T) {
			p := Prepared{Item: Item{PR: 7}, Repository: "org/repo", HeadSHA: newHead, BaseSHA: reviewedHead}
			err := triage(context.Background(), triageInput{prefs: tc.prefs, versions: tc.versions, key: store.Key{Repository: "org/repo", Number: 7}, clone: tc.diff, p: p, backend: tc.backend})
			var unchanged *UnchangedError
			switch {
			case tc.unchanged == "" && err != nil:
				t.Fatalf("review stood down: %v", err)
			case tc.unchanged != "" && (!errors.As(err, &unchanged) || unchanged.Reason != tc.unchanged):
				t.Fatalf("got %v, want unchanged %q", err, tc.unchanged)
			}
			c, ok := tc.backend.(*completerBackend)
			if ok && (c.calls > 0) != tc.asked {
				t.Fatalf("asked %d times, want asked %v", c.calls, tc.asked)
			}
			if ok && c.calls > 0 {
				// The model reads the review's outline (active risks only)
				// and the diff, as data.
				for _, want := range []string{"<review>", "Add retries", "Retry policy", "Retries in step", "<diff>", tc.diff.diff} {
					if !strings.Contains(c.asked.User, want) {
						t.Fatalf("prompt lacks %q:\n%s", want, c.asked.User)
					}
				}
				if strings.Contains(c.asked.User, "Gone") {
					t.Fatal("a mitigated risk reached the prompt")
				}
				if !tc.diff.fetched {
					t.Fatal("the new head was not fetched before the diff")
				}
			}
		})
	}
}

// unchangedExecutor stands every run down as triage does.
type unchangedExecutor struct{ fakeExecutor }

func (u *unchangedExecutor) Execute(_ context.Context, p Prepared, _ func(Progress)) (*float64, error) {
	if !p.Triage {
		return nil, errors.New("the queue did not ask an automatic review to triage")
	}
	return nil, &UnchangedError{Reason: "Only comments changed."}
}

// An automatic review that triage stood down completes, with the agent's
// reason as its summary: the saved review still holds, which is no failure.
func TestUnchangedReviewCompletesWithItsReason(t *testing.T) {
	u := &unchangedExecutor{fakeExecutor{head: "first"}}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), u, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	q.SetAutomaticGate(func(Job) Admission { return Admission{} })
	job, err := q.SubmitAutomatic(context.Background(), AutomaticRequest{Item: Item{Clone: "c", PR: 1}, Repository: "org/repo", Head: "first", Trigger: TriggerNewHead})
	if err != nil {
		t.Fatal(err)
	}
	got := waitFinished(t, q, job.ID)
	if got.State != "completed" || got.Reason != ReasonNoSignificantChange || got.Summary != "Only comments changed." || got.Detail != "" {
		t.Fatalf("job %+v", got)
	}
}
