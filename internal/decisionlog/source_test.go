package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/repos"
)

type sourceRegistry struct {
	*fakeRegistry
	dir string
}

func (r sourceRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return []repos.Repository{{Repository: "acme/widgets", Clones: []repos.Clone{{Path: r.dir, Available: true}}}}, nil
}

// sourceBackend deliberately does not implement agent.Completer: captures
// must use the code-capable task runner, with a real exported source tree.
type sourceBackend struct {
	read  func(agent.Request) (agent.Result, error)
	calls int
}

func (*sourceBackend) Name() string                          { return agent.BackendClaude }
func (*sourceBackend) Check(context.Context) (string, error) { return "1.0.0", nil }
func (b *sourceBackend) Run(_ context.Context, req agent.Request) (agent.Result, error) {
	b.calls++
	return b.read(req)
}

func TestCaptureReadsPinnedRepositorySource(t *testing.T) {
	f := newFixture(t)
	dir := discoveryClone(t)
	f.capturer.source = nil
	f.capturer.Registry = sourceRegistry{fakeRegistry: f.registry, dir: dir}
	f.target.Repository, f.target.Branch = "acme/widgets", "main"
	backend := &sourceBackend{read: func(req agent.Request) (agent.Result, error) {
		code, err := os.ReadFile(filepath.Join(req.Directory, "head", "src", "search", "a.go"))
		if err != nil || string(code) != "package search\n" {
			t.Fatalf("source inaccessible: %q %v", code, err)
		}
		instructions, err := os.ReadFile(filepath.Join(req.Directory, "instructions.txt"))
		if err != nil || !strings.Contains(string(instructions), "extract") {
			t.Fatalf("instructions: %q %v", instructions, err)
		}
		user, err := os.ReadFile(filepath.Join(req.Directory, "transcript.txt"))
		if err != nil || !strings.Contains(string(user), "SQLite") {
			t.Fatalf("transcript: %q %v", user, err)
		}
		if !req.Task || !strings.Contains(req.Prompt, "head/") {
			t.Fatalf("not a code-capable task: %+v", req)
		}
		completion, err := f.backend.Complete(context.Background(), agent.CompleteRequest{User: string(user)})
		data, marshalErr := json.Marshal(sourceReply{Extraction: completion.Document, References: []string{"src/search/a.go"}})
		return agent.Result{Document: data, Calls: 1, Model: completion.Model, Usage: completion.Usage}, errors.Join(err, marshalErr)
	}}
	f.capturer.Backends.Claude = func(string) generate.CheckedBackend { return backend }
	report := f.capture(t, Options{})
	if report.Outcome != OutcomeExtracted || report.Source == nil || len(report.Source.References) != 1 || len(report.Source.Revision) != 40 || report.Source.Resolution != "branch_head" {
		t.Fatalf("%+v", report)
	}
	head, _, err := f.store.Session(context.Background(), f.target.Agent, f.target.ID)
	if err != nil || head.Source == nil || head.Source.Revision != report.Source.Revision || !strings.Contains(head.PromptVersion, sourceContextVersion) {
		t.Fatalf("%+v %v", head, err)
	}
	var data string
	if err := f.store.db.QueryRow(`SELECT source_json FROM decision_source WHERE decision_id=?`, head.Decisions[0].ID).Scan(&data); err != nil || !strings.Contains(data, "src/search/a.go") {
		t.Fatalf("source provenance: %q %v", data, err)
	}
	// A change unrelated to the captured transcript does not remine it.
	write(t, filepath.Join(dir, "new.go"), "package next\n")
	linktest.Git(t, dir, "add", "new.go")
	linktest.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "next")
	f.later(MinInterval)
	if got := f.capture(t, Options{}); got.Outcome != OutcomeUnchanged || backend.calls != 1 {
		t.Fatalf("%+v calls=%d", got, backend.calls)
	}
}

func TestRepositorySourcePinsRecordedCommitAndReportsOmissions(t *testing.T) {
	dir := discoveryClone(t)
	first, err := prepareRepositorySource(context.Background(), dir, "acme/widgets", "", "main", config.Config{Caps: map[string]int{"checkout_max_file_bytes": 10}})
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	if len(first.Context.Omissions) == 0 {
		t.Fatal("large file was not reported as omitted")
	}
	if _, err := os.Stat(filepath.Join(first.Directory, "head", "src", "search", "a.go")); !os.IsNotExist(err) {
		t.Fatalf("omitted file exists: %v", err)
	}
	write(t, filepath.Join(dir, "src", "search", "a.go"), "package changed\n")
	linktest.Git(t, dir, "add", ".")
	linktest.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "changed")
	pinned, err := prepareRepositorySource(context.Background(), dir, "acme/widgets", first.Context.Revision, "main", config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.close()
	got, err := os.ReadFile(filepath.Join(pinned.Directory, "head", "src", "search", "a.go"))
	if err != nil || string(got) != "package search\n" || pinned.Context.Resolution != "recorded_commit" {
		t.Fatalf("%q %+v %v", got, pinned.Context, err)
	}
	if _, err := prepareRepositorySource(context.Background(), dir, "acme/widgets", "", "missing", config.Config{}); !errors.Is(err, errSourceUnavailable) {
		t.Fatalf("missing branch: %v", err)
	}
}

func TestCaptureSourceUnavailableDoesNotCallOrReplaceDecisions(t *testing.T) {
	f := newFixture(t)
	f.capture(t, Options{})
	f.appendTurn(t, 2, "Extend the design.")
	f.later(MinInterval)
	f.capturer.source = nil // fakeRegistry has no registered clones.
	got := f.capture(t, Options{})
	if got.Outcome != OutcomeSkipped || got.Reason != ReasonSourceUnavailable || f.backend.calls != 1 {
		t.Fatalf("%+v calls=%d", got, f.backend.calls)
	}
	head, _, err := f.store.Session(context.Background(), f.target.Agent, f.target.ID)
	if err != nil || len(head.Decisions) != 1 || head.Source == nil {
		t.Fatalf("%+v %v", head, err)
	}
}

func TestContextFreeHeadsMissBothCaptureCaches(t *testing.T) {
	for _, sameFingerprint := range []bool{true, false} {
		t.Run(map[bool]string{true: "fingerprint", false: "content"}[sameFingerprint], func(t *testing.T) {
			f := newFixture(t)
			f.capture(t, Options{})
			if _, err := f.store.db.Exec(`DELETE FROM capture_source`); err != nil {
				t.Fatal(err)
			}
			f.later(MinInterval)
			if !sameFingerprint {
				f.touch(t, 9)
			}
			if got := f.capture(t, Options{}); got.Outcome != OutcomeExtracted || f.backend.calls != 2 {
				t.Fatalf("%+v calls=%d", got, f.backend.calls)
			}
		})
	}
}

func TestSourceReferencesAreValidated(t *testing.T) {
	for _, ref := range []string{"../source.json", "/etc/passwd", "missing.go", "head/src/search/a.go"} {
		t.Run(ref, func(t *testing.T) {
			f := newFixture(t)
			dir := discoveryClone(t)
			f.capturer.source = nil
			f.capturer.Registry = sourceRegistry{fakeRegistry: f.registry, dir: dir}
			f.target.Repository, f.target.Branch = "acme/widgets", "main"
			backend := &sourceBackend{read: func(req agent.Request) (agent.Result, error) {
				user, err := os.ReadFile(filepath.Join(req.Directory, "transcript.txt"))
				if err != nil {
					return agent.Result{}, err
				}
				completion, err := f.backend.Complete(context.Background(), agent.CompleteRequest{User: string(user)})
				data, _ := json.Marshal(sourceReply{Extraction: completion.Document, References: []string{ref}})
				return agent.Result{Document: data, Calls: 1, Usage: completion.Usage}, err
			}}
			f.capturer.Backends.Claude = func(string) generate.CheckedBackend { return backend }
			got, err := f.capturer.Capture(context.Background(), f.target, Options{})
			if err == nil || got.Outcome != OutcomeFailed {
				t.Fatalf("invalid ref accepted: %+v %v", got, err)
			}
			current, err := f.store.Current(context.Background(), SessionKey{f.target.Agent, f.target.ID})
			if err != nil || len(current) != 0 {
				t.Fatalf("invalid source saved: %+v %v", current, err)
			}
		})
	}
}

func TestSourceProvenancePreservesOriginalAndMigratesOldStore(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, &clock{t0})
	meta := Meta{Agent: "claude-code", ID: "session", Repository: "o/r"}
	old := decision("old", "old choice", 1)
	if err := s.Save(ctx, Extraction{Meta: meta, Decisions: []decisions.Decision{old}}); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, dbPath string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE capture_source; DROP TABLE decision_source;`); err != nil {
		t.Fatal(err)
	}
	readonly, err := Open(dbPath, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	head, found, err := readonly.Session(ctx, meta.Agent, meta.ID)
	if err != nil || !found || head.Source != nil || len(head.Decisions) != 1 {
		t.Fatalf("old readonly: %+v %v", head, err)
	}
	if err := readonly.Close(); err != nil {
		t.Fatal(err)
	}
	writable, err := Open(dbPath, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writable.Close() }()
	first := &SourceContext{Version: sourceContextVersion, Repository: "o/r", Revision: "first", References: []string{"first.go"}}
	second := &SourceContext{Version: sourceContextVersion, Repository: "o/r", Revision: "second", References: []string{"second.go"}}
	newChoice := decision("new", "new choice", 2)
	if err := writable.Save(ctx, Extraction{Meta: meta, Decisions: []decisions.Decision{old, newChoice}, Source: first}); err != nil {
		t.Fatal(err)
	}
	if err := writable.Save(ctx, Extraction{Meta: meta, Decisions: []decisions.Decision{old, newChoice, decision("later", "later choice", 3)}, Source: second}); err != nil {
		t.Fatal(err)
	}
	var oldCount int
	if err := writable.db.QueryRow(`SELECT count(*) FROM decision_source WHERE decision_id='old'`).Scan(&oldCount); err != nil || oldCount != 0 {
		t.Fatalf("old decision got fictitious provenance: %d %v", oldCount, err)
	}
	var data string
	if err := writable.db.QueryRow(`SELECT source_json FROM decision_source WHERE decision_id='new'`).Scan(&data); err != nil || !strings.Contains(data, `"revision":"first"`) {
		t.Fatalf("original source overwritten: %q %v", data, err)
	}
	head, _, err = writable.Session(ctx, meta.Agent, meta.ID)
	if err != nil || head.Source == nil || head.Source.Revision != "second" {
		t.Fatalf("new head: %+v %v", head, err)
	}
}

func TestSourcePersistenceFailureRollsBackCapture(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.db.Exec(`CREATE TRIGGER reject_source BEFORE INSERT ON capture_source BEGIN SELECT RAISE(ABORT,'unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.capturer.Capture(context.Background(), f.target, Options{})
	if err == nil {
		t.Fatal("source save unexpectedly succeeded")
	}
	current, err := f.store.Current(context.Background(), SessionKey{f.target.Agent, f.target.ID})
	if err != nil || len(current) != 0 {
		t.Fatalf("partial capture persisted: %+v %v", current, err)
	}
}

func TestCaptureDoesNotReuseSourceFromAnotherRepository(t *testing.T) {
	for _, sameFingerprint := range []bool{true, false} {
		t.Run(map[bool]string{true: "fingerprint", false: "content"}[sameFingerprint], func(t *testing.T) {
			f := newFixture(t)
			f.capture(t, Options{})
			head, _, err := f.store.Session(context.Background(), f.target.Agent, f.target.ID)
			if err != nil {
				t.Fatal(err)
			}
			f.target.Repository = "other/repository"
			prepare := f.capturer.source
			f.capturer.source = func(ctx context.Context, target Target) (repositorySource, error) {
				if target.HeadSHA != "" {
					t.Fatalf("another repository's revision reused: %q", target.HeadSHA)
				}
				return prepare(ctx, target)
			}
			// A metadata update can move the session before extraction catches up;
			// its stored source remains the original repository's until success.
			if err := f.store.Covered(context.Background(), Meta{Agent: f.target.Agent, ID: f.target.ID, Repository: f.target.Repository, Fingerprint: head.Fingerprint, Path: f.path}); err != nil {
				t.Fatal(err)
			}
			f.later(MinInterval)
			if !sameFingerprint {
				f.touch(t, 9)
			}
			got := f.capture(t, Options{})
			if got.Outcome != OutcomeExtracted || got.Source == nil || got.Source.Repository != f.target.Repository || f.backend.calls != 2 {
				t.Fatalf("old repository source reused: %+v calls=%d", got, f.backend.calls)
			}
		})
	}
}

func TestCaptureRetriesMovedRepositoryAfterSourceUnavailable(t *testing.T) {
	f := newFixture(t)
	f.capture(t, Options{})
	f.target.Repository = "other/repository"
	f.later(MinInterval)
	prepare := f.capturer.source
	f.capturer.source = nil
	if got := f.capture(t, Options{}); got.Outcome != OutcomeSkipped || got.Reason != ReasonSourceUnavailable {
		t.Fatalf("%+v", got)
	}
	head, _, err := f.store.Session(context.Background(), f.target.Agent, f.target.ID)
	if err != nil || head.Repository != f.target.Repository || head.Source.Repository == f.target.Repository {
		t.Fatalf("expected moved metadata and old source: %+v %v", head, err)
	}
	f.capturer.source = func(ctx context.Context, target Target) (repositorySource, error) {
		if target.HeadSHA != "" {
			t.Fatalf("old repository revision reused: %q", target.HeadSHA)
		}
		return prepare(ctx, target)
	}
	f.later(24 * time.Hour)
	got := f.capture(t, Options{})
	if got.Outcome != OutcomeExtracted || got.Source == nil || got.Source.Repository != f.target.Repository || f.backend.calls != 2 {
		t.Fatalf("retry reused old source: %+v calls=%d", got, f.backend.calls)
	}
}
