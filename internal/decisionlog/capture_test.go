package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/sessions"
)

const plantedMail = "ada.lovelace@example.com"

// claudeTurn is one prompt of a Claude Code transcript and the agent's reply.
func claudeTurn(session string, n int, prompt, reply string) string {
	at := t0.Add(time.Duration(n) * time.Minute)
	user := fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":"/work/demo","gitBranch":"feature","type":"user","uuid":"u-%d","sessionId":%q,"timestamp":%q,"promptId":"p-%d","message":{"role":"user","content":%q}}`,
		n, session, at.Format(time.RFC3339Nano), n, prompt)
	assistant := fmt.Sprintf(`{"parentUuid":"u-%d","isSidechain":false,"cwd":"/work/demo","gitBranch":"feature","type":"assistant","uuid":"a-%d","sessionId":%q,"timestamp":%q,"requestId":"req-%d","message":{"id":"msg-%d","role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":%q}]}}`,
		n, n, session, at.Add(time.Second).Format(time.RFC3339Nano), n, n, reply)
	return user + "\n" + assistant + "\n"
}

// fakeBackend answers completions with one decision citing the transcript's
// last turn, or fails.
type fakeBackend struct {
	name     string
	checkErr error
	// hang makes the check wait for its context to end.
	hang    bool
	calls   int
	users   []string
	fail    error
	summary string
	onCall  func()
	// hangCall blocks the call until its context ends.
	hangCall bool
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) Check(ctx context.Context) (string, error) {
	if f.hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "1.0.0", f.checkErr
}
func (f *fakeBackend) Run(context.Context, agent.Request) (agent.Result, error) {
	return agent.Result{}, errors.New("not used")
}

var turnPattern = regexp.MustCompile(`## Turn (\d+)`)

func (f *fakeBackend) Complete(ctx context.Context, request agent.CompleteRequest) (agent.Completion, error) {
	f.calls++
	f.users = append(f.users, request.User)
	if f.onCall != nil {
		f.onCall()
	}
	if f.hangCall {
		<-ctx.Done()
		return agent.Completion{}, ctx.Err()
	}
	standard, prompt := "standard", int64(1000)
	completion := agent.Completion{Model: "claude-haiku-5-5", Usage: agent.Usage{Input: 1000, Output: 100, Speed: &standard, MinPrompt: &prompt, MaxPrompt: &prompt}}
	if f.fail != nil {
		return completion, f.fail
	}
	last := 0
	for _, match := range turnPattern.FindAllStringSubmatch(request.User, -1) {
		last, _ = strconv.Atoi(match[1])
	}
	summary := f.summary
	if summary == "" {
		summary = fmt.Sprintf("Decided in turn %d.", last)
	}
	completion.Document = fmt.Appendf(nil, `{"decisions":[{"title":"T","summary":%q,"who":"human","category":"implementation","trigger":"request","rationale":"","rejected_alternative":"","superseded":false,"turns":[%d],"decided_turn":%d,"evidence":"","files":[]}],"superseded_existing":[]}`,
		summary, last, last)
	return completion, nil
}

type fakeRegistry struct {
	prefs    repos.Preferences
	settings repos.Settings
	picked   bool
}

func (r *fakeRegistry) Preferences(context.Context) (repos.Preferences, error) { return r.prefs, nil }
func (r *fakeRegistry) RepositorySettings(context.Context, string) (repos.Settings, bool, error) {
	return r.settings, r.picked, nil
}

type fixture struct {
	home, path string
	clock      *clock
	backend    *fakeBackend
	registry   *fakeRegistry
	store      *Store
	capturer   *Capturer
	target     Target
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{home: home, clock: &clock{t0.Add(time.Hour)}, backend: &fakeBackend{name: agent.BackendClaude},
		registry: &fakeRegistry{prefs: repos.DefaultPreferences(), picked: true}}
	f.path = filepath.Join(home, ".claude", "projects", "-work-demo", "s1.jsonl")
	write(t, f.path, claudeTurn("s1", 1, "Use SQLite for the cache, not Redis.", "Done."))
	f.store = openStore(t, f.clock)
	build := func(string) generate.CheckedBackend { return f.backend }
	f.capturer = &Capturer{Store: f.store, Registry: f.registry, Config: config.Config{Backend: config.BackendClaude}, Home: home, Now: f.clock.Now,
		Backends: generate.Backends{Claude: build, Codex: build, Anthropic: build, CursorAgent: build}}
	f.target = Target{Agent: "claude-code", ID: "s1", Path: f.path, Repository: "o/r", Branch: "feature"}
	return f
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) appendTurn(t *testing.T, n int, prompt string) {
	t.Helper()
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(claudeTurn("s1", n, prompt, "Done."))
	_ = file.Close()
	f.touch(t, n)
}

func (f *fixture) touch(t *testing.T, n int) {
	t.Helper()
	later := time.Now().Add(time.Duration(n) * time.Second)
	if err := os.Chtimes(f.path, later, later); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) capture(t *testing.T, o Options) Report {
	t.Helper()
	report, err := f.capturer.Capture(context.Background(), f.target, o)
	if err != nil {
		t.Fatalf("capture: %v (%+v)", err, report)
	}
	return report
}

func (f *fixture) later(d time.Duration) { f.clock.now = f.clock.now.Add(d) }

func TestCaptureExtractsExtendsAndReuses(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	report := f.capture(t, Options{})
	if report.Outcome != OutcomeExtracted || report.Mode != "full" || report.Decisions != 1 || report.Turns != 1 || f.backend.calls != 1 || report.Cost == nil || report.Reserved <= 0 {
		t.Fatalf("%+v", report)
	}
	first, _ := f.store.Current(ctx, SessionKey{"claude-code", "s1"})
	if len(first) != 1 || first[0].Repository != "o/r" || first[0].Branch != "feature" || !first[0].DecidedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%+v", first)
	}

	f.later(time.Minute)
	f.appendTurn(t, 2, "Keep the TTL at one hour.")
	if report := f.capture(t, Options{}); report.Outcome != OutcomeThrottled {
		t.Fatalf("%+v", report)
	}
	f.later(MinInterval)
	if report := f.capture(t, Options{}); report.Outcome != OutcomeExtracted || report.Mode != "extend" || report.Decisions != 2 {
		t.Fatalf("%+v", report)
	}
	if !strings.Contains(f.backend.users[1], "# Existing decisions") {
		t.Fatal("the extension did not show the existing decisions")
	}
	current, _ := f.store.Current(ctx, SessionKey{"claude-code", "s1"})
	if len(current) != 2 || current[0].ID != first[0].ID {
		t.Fatalf("%+v", current)
	}

	f.later(MinInterval)
	if report := f.capture(t, Options{}); report.Outcome != OutcomeUnchanged {
		t.Fatalf("%+v", report)
	}
	f.touch(t, 9) // a new fingerprint, the same transcript
	if report := f.capture(t, Options{}); report.Outcome != OutcomeHit || f.backend.calls != 2 {
		t.Fatalf("%+v calls=%d", report, f.backend.calls)
	}
	var succeeded int
	_ = f.store.db.QueryRow(`SELECT COUNT(*) FROM capture_attempt WHERE state=? AND cost_usd IS NOT NULL`, AttemptSucceeded).Scan(&succeeded)
	if succeeded != 2 {
		t.Fatalf("%d succeeded attempts", succeeded)
	}
}

func TestCaptureGate(t *testing.T) {
	off := false
	for name, edit := range map[string]func(*fakeRegistry){
		"paused":         func(r *fakeRegistry) { r.prefs.Automatic = false },
		"decisions off":  func(r *fakeRegistry) { r.prefs.Decisions.Enabled = &off },
		"repository off": func(r *fakeRegistry) { r.settings.Decisions = &off },
		"not picked":     func(r *fakeRegistry) { r.picked = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			edit(f.registry)
			if report := f.capture(t, Options{}); report.Outcome != OutcomeDisabled || f.backend.calls != 0 {
				t.Fatalf("%+v", report)
			}
			if _, found, _ := f.store.Session(context.Background(), "claude-code", "s1"); found {
				t.Fatal("a disabled session was recorded")
			}
		})
	}
}

// TestCaptureRunsUnpricedModel: prices only report, so a model the price list
// does not price is called like any other, reserved at 0.
func TestCaptureRunsUnpricedModel(t *testing.T) {
	f := newFixture(t)
	unknown := "no-such-model"
	f.registry.prefs.Decisions.Model = &unknown
	report := f.capture(t, Options{})
	if report.Outcome != OutcomeExtracted || report.Model != unknown || f.backend.calls != 1 || report.Reserved != 0 {
		t.Fatalf("%+v", report)
	}
}

func TestCaptureSkips(t *testing.T) {
	cursor := config.BackendCursorAgent
	cases := map[string]struct {
		edit func(*testing.T, *fixture)
		want string
	}{
		"cursor-agent": {func(_ *testing.T, f *fixture) { f.registry.prefs.Backend = &cursor }, ReasonUnsupported},
		"generation run": {func(t *testing.T, f *fixture) {
			write(t, f.path, claudeTurn("s1", 1, sessions.GenerationRunMarker+"\nReview the pull request.", "Done."))
		}, ReasonGenerationRun},
		"other session": {func(t *testing.T, f *fixture) { write(t, f.path, claudeTurn("s2", 1, "Hello.", "Hi.")) }, ReasonMismatch},
		"too large": {func(t *testing.T, f *fixture) {
			if err := os.Truncate(f.path, MaxTranscriptBytes+1); err != nil {
				t.Fatal(err)
			}
		}, ReasonTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			tc.edit(t, f)
			report := f.capture(t, Options{})
			if report.Outcome != OutcomeSkipped || report.Reason != tc.want || f.backend.calls != 0 {
				t.Fatalf("%+v", report)
			}
			row, _, _ := f.store.Session(context.Background(), "claude-code", "s1")
			if row.SkipReason != tc.want || row.Coverage != CoverageSkipped {
				t.Fatalf("%+v", row)
			}
			if tc.want == ReasonMismatch && row.Path != "" {
				t.Fatalf("a transcript of another session was stored: %q", row.Path)
			}
		})
	}
}

func TestCaptureRefusesFilesOutsideTheStore(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "s1.jsonl")
	write(t, outside, claudeTurn("s1", 1, "Hello.", "Hi."))
	link := filepath.Join(f.home, ".claude", "projects", "-work-demo", "link.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, link, "relative.jsonl"} {
		f.target.Path = path
		if report := f.capture(t, Options{}); report.Outcome != OutcomeUnavailable || f.backend.calls != 0 {
			t.Fatalf("%s: %+v", path, report)
		}
	}
}

func TestCaptureFailuresBackOffAndStop(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.backend.fail = errors.New("provider failed")
	for i := range MaxFailures {
		report, err := f.capturer.Capture(ctx, f.target, Options{})
		if err == nil || report.Outcome != OutcomeFailed {
			t.Fatalf("%d: %+v %v", i, report, err)
		}
		if report, _ := f.capturer.Capture(ctx, f.target, Options{}); report.Outcome != OutcomeThrottled || report.Reason != "failed_recently" {
			t.Fatalf("%+v", report)
		}
		f.later(FailureDelay)
	}
	row, _, _ := f.store.Session(ctx, "claude-code", "s1")
	if row.SkipReason != ReasonFailed {
		t.Fatalf("%+v", row)
	}
	if report := f.capture(t, Options{}); report.Outcome != OutcomeUnchanged || f.backend.calls != MaxFailures {
		t.Fatalf("%+v", report)
	}
	var unknown int
	_ = f.store.db.QueryRow(`SELECT COUNT(*) FROM capture_attempt WHERE state=? AND cost_usd IS NOT NULL`, AttemptFailed).Scan(&unknown)
	if unknown != MaxFailures {
		t.Fatalf("%d failed attempts priced from their usage", unknown)
	}
	f.backend.fail = nil
	f.appendTurn(t, 2, "Try again.")
	if report := f.capture(t, Options{}); report.Outcome != OutcomeExtracted {
		t.Fatalf("%+v", report)
	}
}

func TestCaptureMasksSecretsInTheReplyAndDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	report := f.capture(t, Options{DryRun: true})
	if report.Outcome != OutcomeDryRun || report.Tokens <= 0 || report.MaxTokens < report.Tokens || report.Reserved <= 0 || f.backend.calls != 0 {
		t.Fatalf("%+v", report)
	}
	if _, found, _ := f.store.Session(ctx, "claude-code", "s1"); found {
		t.Fatal("a dry run wrote the session")
	}
	f.backend.summary = "Mail " + plantedMail + " about it."
	f.capture(t, Options{})
	current, _ := f.store.Current(ctx, SessionKey{"claude-code", "s1"})
	if len(current) != 1 || strings.Contains(current[0].Summary, plantedMail) {
		t.Fatalf("%+v", current)
	}
}

func TestCaptureAutoFollowsTheReviewBackendSelection(t *testing.T) {
	missing := &fakeBackend{name: agent.BackendClaude, checkErr: errors.New("claude is not installed")}
	codex := &fakeBackend{name: agent.BackendCodex}
	anthropic := &fakeBackend{name: agent.BackendAnthropic}
	cursor := &fakeBackend{name: agent.BackendCursorAgent}
	for name, tc := range map[string]struct {
		claude  *fakeBackend
		backend string
		model   string
		called  *fakeBackend
	}{
		"claude first":         {&fakeBackend{name: agent.BackendClaude}, agent.BackendClaude, "claude-haiku-5-5", nil},
		"codex when no claude": {missing, agent.BackendCodex, "gpt-6-luna", codex},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.capturer.Config = config.Config{Backend: config.BackendAuto}
			f.capturer.Backends = generate.Backends{
				Claude:      func(string) generate.CheckedBackend { return tc.claude },
				Codex:       func(string) generate.CheckedBackend { return codex },
				Anthropic:   func(string) generate.CheckedBackend { return anthropic },
				CursorAgent: func(string) generate.CheckedBackend { return cursor },
			}
			called := tc.called
			if called == nil {
				called = tc.claude
			}
			before := called.calls
			report := f.capture(t, Options{})
			if report.Outcome != OutcomeExtracted || report.Backend != tc.backend || report.Model != tc.model || called.calls != before+1 || cursor.calls != 0 {
				t.Fatalf("%+v", report)
			}
			row, _, _ := f.store.Session(context.Background(), "claude-code", "s1")
			if row.Backend != tc.backend {
				t.Fatalf("stored backend %q", row.Backend)
			}
		})
	}
	t.Run("nothing usable", func(t *testing.T) {
		f := newFixture(t)
		f.capturer.Config = config.Config{Backend: config.BackendAuto}
		failing := func(name string) func(string) generate.CheckedBackend {
			return func(string) generate.CheckedBackend { return &fakeBackend{name: name, checkErr: errors.New("missing")} }
		}
		f.capturer.Backends = generate.Backends{Claude: failing(agent.BackendClaude), Codex: failing(agent.BackendCodex),
			Anthropic: failing(agent.BackendAnthropic), CursorAgent: func(string) generate.CheckedBackend { return cursor }}
		if report := f.capture(t, Options{}); report.Outcome != OutcomeSkipped || report.Reason != ReasonUnavailable || cursor.calls != 0 {
			t.Fatalf("%+v", report)
		}
	})
}

func TestCaptureCancelledIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFixture(t)
	f.backend.fail = context.Canceled
	f.backend.onCall = cancel
	report, err := f.capturer.Capture(ctx, f.target, Options{})
	if !errors.Is(err, context.Canceled) || report.Outcome != OutcomeAbandoned || report.Reason != "cancelled" {
		t.Fatalf("%+v %v", report, err)
	}
	var state string
	_ = f.store.db.QueryRow(`SELECT state FROM capture_attempt`).Scan(&state)
	row, _, _ := f.store.Session(context.Background(), "claude-code", "s1")
	if state != AttemptAbandoned || row.Failures != 0 {
		t.Fatalf("state %q, failures %d", state, row.Failures)
	}
	f.backend.fail, f.backend.onCall = nil, nil
	if report := f.capture(t, Options{Force: true}); report.Outcome != OutcomeExtracted {
		t.Fatalf("%+v", report)
	}
}

func TestCaptureHungCallTimesOutAsAFailure(t *testing.T) {
	f := newFixture(t)
	f.backend.hangCall = true
	f.capturer.CallTimeout = 50 * time.Millisecond
	report, err := f.capturer.Capture(context.Background(), f.target, Options{})
	if err == nil || report.Outcome != OutcomeFailed || report.Reason != "timeout" {
		t.Fatalf("%+v %v", report, err)
	}
	var state, reason string
	_ = f.store.db.QueryRow(`SELECT state, reason FROM capture_attempt`).Scan(&state, &reason)
	row, _, _ := f.store.Session(context.Background(), "claude-code", "s1")
	if state != AttemptFailed || reason != "timeout" || row.Failures != 1 {
		t.Fatalf("state %q reason %q, failures %d", state, reason, row.Failures)
	}
}

func TestDecisionModelFollowsTheBackend(t *testing.T) {
	list := pricing.Active()
	claudeModel, gptModel, unknown := "claude-sonnet-5-5", "gpt-6-luna", "house-model"
	cases := []struct {
		backend string
		model   *string
		want    string
	}{
		{agent.BackendClaude, nil, "claude-haiku-5-5"},
		{agent.BackendCodex, nil, "gpt-6-luna"},
		{agent.BackendClaude, &claudeModel, claudeModel},
		{agent.BackendCodex, &claudeModel, "gpt-6-luna"},
		{agent.BackendAnthropic, &gptModel, "claude-haiku-5-5"},
		{agent.BackendCodex, &unknown, unknown},
	}
	for _, tc := range cases {
		if got := decisionModel(list, tc.backend, tc.model); got != tc.want {
			t.Errorf("%s %v: %q, want %q", tc.backend, tc.model, got, tc.want)
		}
	}
}

// TestReservationCountsClaudesRetry prices a session as Capture reserves it
// (decisions.Extractor.MaxTokens: one token per byte of the prompt, the
// transcript and the schema, plus framing).
func TestReservationCountsClaudesRetry(t *testing.T) {
	extractor := newExtractor()
	schema, _ := json.Marshal(decisions.ExtractSchema())
	bound := func(rendered int) int64 {
		return int64(len(extractor.SystemPrompt()) + rendered + len(schema) + decisions.FramingTokens)
	}
	// claude caches its prompt and may retry once with the cached prompt:
	// more than one plain call, less than two.
	single, _ := reserve(pricing.Active(), agent.BackendAnthropic, "claude-haiku-5-5", bound(250_000))
	double, _ := reserve(pricing.Active(), agent.BackendClaude, "claude-haiku-5-5", bound(250_000))
	if double <= single || double >= 2*single {
		t.Fatalf("claude reserves $%.4f against one call's $%.4f", double, single)
	}
}

// TestCostFollowsReviewAccounting: usage review accounting cannot price is
// unknown, and usage of more than one request is not priced as one.
func TestCostFollowsReviewAccounting(t *testing.T) {
	list := pricing.Active()
	standard := "standard"
	n := func(v int64) *int64 { return &v }
	priced := agent.Usage{Input: 1000, Output: 100, Speed: &standard, MinPrompt: n(1000), MaxPrompt: n(1000)}
	if cost(list, "claude-haiku-5-5", priced) == nil {
		t.Fatal("reported usage is not priced")
	}
	noSpeed := priced
	noSpeed.Speed = nil
	noSplit := priced
	noSplit.CacheCreate = 500
	if c := cost(list, "claude-haiku-5-5", noSpeed); c != nil {
		t.Fatalf("usage without a speed priced at $%v", *c)
	}
	if c := cost(list, "claude-haiku-5-5", noSplit); c != nil {
		t.Fatalf("cache writes without a 1-hour split priced at $%v", *c)
	}
	// Two 60,000-token calls: claude-haiku-5-5's long-prompt tier starts at
	// 100,000 tokens, so the combined 120,000 must not select it.
	two := agent.Usage{Input: 120_000, Output: 200, Speed: &standard}
	if c := cost(list, "claude-haiku-5-5", two); c != nil {
		t.Fatalf("two calls without a prompt range priced as one at $%v", *c)
	}
	two.MinPrompt, two.MaxPrompt = n(60_000), n(60_000)
	c := cost(list, "claude-haiku-5-5", two)
	if want := (120_000*0.1 + 200*0.5) / 1e6; c == nil || math.Abs(*c-want) > 1e-12 {
		t.Fatalf("two calls priced at %v, want $%v at the base tier", c, want)
	}
}

func TestDebugDirKeepsOnlyThisCapturesFiles(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "debug")
	f.capture(t, Options{DebugDir: dir})
	if _, err := os.Stat(filepath.Join(dir, "reply.json")); err != nil {
		t.Fatal(err)
	}
	f.capture(t, Options{DebugDir: dir, DryRun: true, Force: true})
	for name, want := range map[string]bool{"transcript.txt": true, "user.txt": false, "reply.json": false} {
		info, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s: %v", name, err)
		} else if want && info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
}

// A capture keeps its extraction when the ledger cannot be written after
// the call.
func TestCaptureKeepsTheExtractionWhenTheLedgerFails(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.db.Exec(`CREATE TRIGGER no_settle BEFORE UPDATE ON capture_attempt BEGIN SELECT RAISE(ABORT, 'read-only ledger'); END`); err != nil {
		t.Fatal(err)
	}
	report := f.capture(t, Options{})
	current, _ := f.store.Current(context.Background(), SessionKey{"claude-code", "s1"})
	if report.Outcome != OutcomeExtracted || len(current) != 1 {
		t.Fatalf("%+v, %d decisions", report, len(current))
	}
}

// codexRollout writes a root Codex rollout of session id under dir in the
// fixture's store and returns its path.
func (f *fixture) codexRollout(t *testing.T, dir, id string) string {
	t.Helper()
	path := filepath.Join(f.home, ".codex", "sessions", "2026", "10", dir, "rollout-2026-10-20T09-00-00-"+id+".jsonl")
	write(t, path, `{"timestamp":"2026-10-20T09:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/demo","cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-10-20T09:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"Keep the flag name."}}
`)
	return path
}

const codexID = "019f0000-0000-7000-8000-000000000001"

// codexFixture tracks a Codex session whose hooks name no transcript, and
// counts the lookups by id, which scan the store.
func codexFixture(t *testing.T) (*fixture, *int, *[]string) {
	f := newFixture(t)
	scans, paths := 0, []string{}
	f.capturer.locate = func(home, cursorStore, agent, id, path string) (livesessions.Location, error) {
		if path == "" {
			scans++
		}
		paths = append(paths, path)
		return livesessions.Locate(home, cursorStore, agent, id, path)
	}
	f.target = Target{Agent: "codex", ID: codexID, Repository: "o/r"}
	return f, &scans, &paths
}

func (f *fixture) storedPath(t *testing.T) string {
	t.Helper()
	row, _, err := f.store.Session(context.Background(), "codex", codexID)
	if err != nil {
		t.Fatal(err)
	}
	return row.Path
}

func (f *fixture) setStoredPath(t *testing.T, path string) {
	t.Helper()
	if _, err := f.store.db.Exec(`UPDATE capture_session SET transcript_path=? WHERE agent='codex'`, path); err != nil {
		t.Fatal(err)
	}
}

// A Codex session whose hooks named no transcript is looked up in the store
// once; later captures read the path the first one stored.
func TestCaptureLooksACodexSessionUpOnce(t *testing.T) {
	f, scans, paths := codexFixture(t)
	rollout := f.codexRollout(t, "20", codexID)
	if report := f.capture(t, Options{}); report.Outcome != OutcomeExtracted {
		t.Fatalf("%+v", report)
	}
	if got := f.storedPath(t); got != rollout {
		t.Fatalf("stored path %q", got)
	}
	f.later(MinInterval)
	if report := f.capture(t, Options{}); report.Outcome != OutcomeUnchanged {
		t.Fatalf("%+v", report)
	}
	if *scans != 1 || len(*paths) != 2 || (*paths)[1] != rollout {
		t.Fatalf("%d scans, paths %q", *scans, *paths)
	}
}

func TestCaptureLooksASessionUpAgainWhenItsStoredPathFails(t *testing.T) {
	cases := map[string]func(t *testing.T, f *fixture, rollout string){
		// Codex moved the rollout: the old path is gone.
		"moved": func(t *testing.T, f *fixture, rollout string) {
			if err := os.Remove(rollout); err != nil {
				t.Fatal(err)
			}
		},
		"outside the store": func(t *testing.T, f *fixture, _ string) {
			outside := filepath.Join(t.TempDir(), "rollout-2026-10-20T09-00-00-"+codexID+".jsonl")
			write(t, outside, "{}\n")
			f.setStoredPath(t, outside)
		},
		"another session": func(t *testing.T, f *fixture, _ string) {
			f.setStoredPath(t, f.codexRollout(t, "21", "019f0000-0000-7000-8000-000000000002"))
		},
	}
	for name, edit := range cases {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, force %v", name, force), func(t *testing.T) {
				f, scans, _ := codexFixture(t)
				rollout := f.codexRollout(t, "20", codexID)
				if report := f.capture(t, Options{}); report.Outcome != OutcomeExtracted {
					t.Fatalf("%+v", report)
				}
				edit(t, f, rollout)
				want := rollout
				if name == "moved" {
					want = f.codexRollout(t, "22", codexID)
				}
				f.later(MinInterval)
				// A rollout that moved has a new fingerprint and is a hit;
				// the file the head covers, found again, is unchanged.
				// Either way the stored path is repaired.
				report := f.capture(t, Options{Force: force})
				outcome := OutcomeUnchanged
				if force || name == "moved" {
					outcome = OutcomeHit
				}
				if report.Outcome != outcome || *scans != 2 {
					t.Fatalf("%+v, %d scans", report, *scans)
				}
				if got := f.storedPath(t); got != want {
					t.Fatalf("stored path %q, want %q", got, want)
				}
			})
		}
	}
}

func TestCaptureForgetsAStoredPathShownWrong(t *testing.T) {
	f, _, _ := codexFixture(t)
	rollout := f.codexRollout(t, "20", codexID)
	if report := f.capture(t, Options{}); report.Outcome != OutcomeExtracted {
		t.Fatalf("%+v", report)
	}
	f.setStoredPath(t, f.codexRollout(t, "21", "019f0000-0000-7000-8000-000000000002"))
	if err := os.Truncate(rollout, MaxTranscriptBytes+1); err != nil {
		t.Fatal(err)
	}
	f.later(MinInterval)
	if report := f.capture(t, Options{}); report.Outcome != OutcomeSkipped || report.Reason != ReasonTooLarge {
		t.Fatalf("%+v", report)
	}
	if got := f.storedPath(t); got != "" {
		t.Fatalf("stored path %q", got)
	}
}

func TestCaptureKeepsTheFirstLookupError(t *testing.T) {
	f, scans, _ := codexFixture(t)
	f.codexRollout(t, "20", codexID)
	if report := f.capture(t, Options{}); report.Outcome != OutcomeExtracted {
		t.Fatalf("%+v", report)
	}
	if err := os.RemoveAll(filepath.Join(f.home, ".codex", "sessions", "2026")); err != nil {
		t.Fatal(err)
	}
	f.later(MinInterval)
	report := f.capture(t, Options{})
	if report.Outcome != OutcomeUnavailable || report.Reason != "The transcript file is not there any more." || *scans != 2 {
		t.Fatalf("%+v, %d scans", report, *scans)
	}
}

// The model the app shows is the one a capture would use, auto resolved by
// the backends' checks.
func TestEffectiveModelResolvesAsCaptureDoes(t *testing.T) {
	ctx := context.Background()
	missing := func(name string) func(string) generate.CheckedBackend {
		return func(string) generate.CheckedBackend { return &fakeBackend{name: name, checkErr: errors.New("missing")} }
	}
	present := func(name string) func(string) generate.CheckedBackend {
		return func(string) generate.CheckedBackend { return &fakeBackend{name: name} }
	}
	noClaude := generate.Backends{Claude: missing(agent.BackendClaude), Codex: present(agent.BackendCodex),
		Anthropic: missing(agent.BackendAnthropic), CursorAgent: present(agent.BackendCursorAgent)}
	sonnet, house := "claude-sonnet-5-5", "house-model"
	auto, cursor, codex, claude := config.BackendAuto, config.BackendCursorAgent, config.BackendCodex, config.BackendClaude
	for name, tc := range map[string]struct {
		prefs repos.Preferences
		want  Effective
	}{
		"auto picks codex, default model": {repos.Preferences{Backend: &auto}, Effective{Backend: agent.BackendCodex, Model: "gpt-6-luna"}},
		"a claude model is ignored with codex": {repos.Preferences{Backend: &auto, Decisions: repos.DecisionPreferences{Model: &sonnet}},
			Effective{Backend: agent.BackendCodex, Model: "gpt-6-luna", Ignored: true}},
		"an unknown family is kept": {repos.Preferences{Backend: &auto, Decisions: repos.DecisionPreferences{Model: &house}},
			Effective{Backend: agent.BackendCodex, Model: house}},
		"cursor-agent captures nothing": {repos.Preferences{Backend: &cursor, Decisions: repos.DecisionPreferences{Model: &sonnet}},
			Effective{Backend: agent.BackendCursorAgent}},
		"its own backend over the reviews'": {repos.Preferences{Backend: &cursor, Decisions: repos.DecisionPreferences{Backend: &codex}},
			Effective{Backend: agent.BackendCodex, Model: "gpt-6-luna"}},
		"its own backend with its model": {repos.Preferences{Backend: &auto, Decisions: repos.DecisionPreferences{Backend: &claude, Model: &sonnet}},
			Effective{Backend: agent.BackendClaude, Model: sonnet}},
	} {
		if got := EffectiveModel(ctx, config.Config{Backend: config.BackendClaude}, tc.prefs, noClaude, pricing.Active()); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	// Without a backend nothing is known, and a saved model is not called
	// ignored.
	none := generate.Backends{Claude: missing(agent.BackendClaude), Codex: missing(agent.BackendCodex), Anthropic: missing(agent.BackendAnthropic), CursorAgent: present(agent.BackendCursorAgent)}
	saved := repos.Preferences{Decisions: repos.DecisionPreferences{Model: &sonnet}}
	if got := EffectiveModel(ctx, config.Config{Backend: config.BackendAuto}, saved, none, nil); got != (Effective{}) {
		t.Errorf("nothing usable: %+v", got)
	}
	// A check that does not answer in time is unknown too.
	effectiveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { effectiveTimeout = 5 * time.Second })
	hanging := none
	hanging.Claude = func(string) generate.CheckedBackend { return &fakeBackend{name: agent.BackendClaude, hang: true} }
	if got := EffectiveModel(ctx, config.Config{Backend: config.BackendAuto}, saved, hanging, nil); got != (Effective{Undetermined: true}) {
		t.Errorf("a hanging check: %+v", got)
	}
}
