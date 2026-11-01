package generate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

func chmodTree(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}

func keptOptions(t *testing.T, f *fixture) (generate.Options, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "review-runs")
	t.Cleanup(func() { chmodTree(root) })
	o := f.options(t)
	o.KeepSession, o.SessionRoot = true, root
	return o, root
}

func TestKeepSessionRecordsTheSessionAndKeepsTheDirectory(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	o, root := keptOptions(t, f)
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(ctx, s, key, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !f.backend.Requests[0].PersistSession {
		t.Error("the backend was not asked to persist its session")
	}
	latest, err := s.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	session := latest.Metadata.Session
	resolved, _ := filepath.EvalSymlinks(root)
	if session == nil || session.Backend != "claude" || session.ID != generatetest.SessionID || filepath.Dir(session.Directory) != resolved || !strings.HasPrefix(filepath.Base(session.Directory), agent.RunDirPrefix) {
		t.Fatalf("session = %+v, root %s", session, root)
	}
	if session.Directory != result.RunDir {
		t.Errorf("run dir %q, session dir %q", result.RunDir, session.Directory)
	}
	if info, err := os.Stat(session.Directory); err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("kept directory: %v %v", info, err)
	}
	privacy := filepath.Join(session.Directory, "privacy-test")
	if err := os.WriteFile(privacy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(privacy); err != nil || !privatefs.PrivateFile(privacy, info) {
		t.Fatalf("kept directory did not keep its file private: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(session.Directory, "bundle.json")); err != nil {
		t.Error(err)
	}
	versions, err := s.Versions(ctx, key)
	if err != nil || versions[0].Session == nil || versions[0].Session.ID != generatetest.SessionID {
		t.Errorf("versions: %+v %v", versions, err)
	}
	f.requireClean(t)
}

func TestWithoutKeepSessionTheDirectoryGoesAndNoSessionIsRecorded(t *testing.T) {
	f := newFixture(t)
	result, err := generate.Run(context.Background(), f.options(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.backend.Requests[0].PersistSession || result.Metadata.Session != nil || result.RunDir != "" {
		t.Errorf("persist=%v session=%+v dir=%q", f.backend.Requests[0].PersistSession, result.Metadata.Session, result.RunDir)
	}
	f.requireClean(t)
}

func TestKeepSessionRemovesTheDirectoryOnFailure(t *testing.T) {
	f := newFixture(t)
	o, root := keptOptions(t, f)
	f.backend.RunFunc = func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		document, err := generatetest.Document(request.Directory, func(d map[string]any) { d["sections"] = []any{} })
		return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 50, SessionID: generatetest.SessionID}, err
	}
	if _, err := generate.Run(context.Background(), o); err == nil {
		t.Fatal("a rejected document succeeded")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("left in the root: %v", entries)
	}
}

func TestKeepSessionRemovesTheDirectoryWithoutASessionID(t *testing.T) {
	f := newFixture(t)
	o, root := keptOptions(t, f)
	f.backend.RunFunc = func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		document, err := generatetest.Document(request.Directory, nil)
		return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 50}, err
	}
	result, err := generate.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if result.Metadata.Session != nil || len(entries) != 0 {
		t.Errorf("session %+v, left %v", result.Metadata.Session, entries)
	}
}

// The session recorded is the one of the call that produced the accepted
// document: a repair is its own call.
func TestKeepSessionRecordsTheRepairsSession(t *testing.T) {
	f := newFixture(t)
	o, _ := keptOptions(t, f)
	f.backend.RunFunc = func(_ context.Context, request agent.Request, n int) (agent.Result, error) {
		document, err := generatetest.Document(request.Directory, func(d map[string]any) {
			if n == 0 {
				d["sections"] = []any{}
			}
		})
		return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 50, SessionID: []string{"first", "repair"}[n]}, err
	}
	result, err := generate.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.Session == nil || result.Metadata.Session.ID != "repair" {
		t.Errorf("session = %+v", result.Metadata.Session)
	}
}

func TestKeepSessionRemovesTheDirectoryWhenStoringFails(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	o, root := keptOptions(t, f)
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	if _, err = generate.Publish(ctx, failingPut{s}, key, o, nil); err == nil {
		t.Fatal("publish succeeded")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("left in the root: %v", entries)
	}
}

type failingPut struct{ *store.Store }

func (failingPut) Put(context.Context, store.Key, store.Version, store.Version) error {
	return os.ErrPermission
}

// claudeHome points Claude Code's session store at a temporary directory and
// returns a function that writes a session the way the CLI does.
func claudeHome(t *testing.T) func(dir, id string) string {
	t.Helper()
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CODEX_HOME", t.TempDir())
	return func(dir, id string) string {
		path := filepath.Join(config, "projects", claudecode.EncodeProjectDir(dir), id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"type":"user","cwd":%q,"sessionId":%q}`+"\n", dir, id)), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

// Every call persists a session; only the one that produced the accepted
// document stays, and a failed run leaves none of them.
func TestKeepSessionDeletesTheCLISessionsItDoesNotKeep(t *testing.T) {
	write := claudeHome(t)
	for _, test := range []struct {
		name   string
		reject bool
	}{{"repaired", false}, {"rejected", true}} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			o, _ := keptOptions(t, f)
			var files []string
			f.backend.RunFunc = func(_ context.Context, request agent.Request, n int) (agent.Result, error) {
				id := fmt.Sprintf("s-%s-%d", test.name, n)
				files = append(files, write(request.Directory, id))
				document, err := generatetest.Document(request.Directory, func(d map[string]any) {
					if n == 0 || test.reject {
						d["sections"] = []any{}
					}
				})
				return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 50, SessionID: id}, err
			}
			result, err := generate.Run(context.Background(), o)
			if test.reject {
				if err == nil {
					t.Fatal("a rejected run succeeded")
				}
				for _, path := range files {
					if exists(path) {
						t.Errorf("%s stayed after a rejected run", path)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 || exists(files[0]) || !exists(files[1]) || result.Metadata.Session.ID != "s-repaired-1" {
				t.Errorf("files %v, session %+v", files, result.Metadata.Session)
			}
			// The rejected document of the repair is raw model output.
			if left, _ := filepath.Glob(filepath.Join(result.RunDir, "rejected-*.json")); len(left) != 0 {
				t.Errorf("kept the rejected documents: %v", left)
			}
		})
	}
}

// A cache directory that cannot be written to must not fail the review.
func TestKeepSessionFallsBackWhenTheRootIsUnusable(t *testing.T) {
	f := newFixture(t)
	o := f.options(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	o.KeepSession, o.SessionRoot = true, filepath.Join(blocker, "review-runs")
	result, err := generate.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.Session != nil || f.backend.Requests[0].PersistSession || result.RunDir != "" {
		t.Errorf("session %+v persist %v dir %q", result.Metadata.Session, f.backend.Requests[0].PersistSession, result.RunDir)
	}
	if !strings.Contains(f.progress.String(), "warning: not keeping the agent session") {
		t.Errorf("no warning:\n%s", f.progress.String())
	}
	f.requireClean(t)
}

// A review group never keeps a session, whatever the option says.
func TestGroupReviewsNeverKeepASession(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(t.TempDir(), "review-runs")
	o := f.groupOptions(t)
	o.KeepSession, o.SessionRoot = true, root
	result, err := generate.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.Session != nil || f.backend.Requests[0].PersistSession || exists(root) {
		t.Errorf("session %+v persist %v", result.Metadata.Session, f.backend.Requests[0].PersistSession)
	}
	f.requireClean(t)
}

// cancelOnDone cancels the run when its progress reports completion, which is
// after the run kept its directory and before Publish stores the version.
type cancelOnDone struct {
	cancel context.CancelFunc
	buf    bytes.Buffer
}

func (c *cancelOnDone) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("generated in")) {
		c.cancel()
	}
	return c.buf.Write(p)
}

func TestPublishInterruptedAfterTheRunRemovesTheKeptRunAndItsSessions(t *testing.T) {
	write := claudeHome(t)
	f := newFixture(t)
	o, root := keptOptions(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.Progress = &cancelOnDone{cancel: cancel}
	var session string
	f.backend.RunFunc = func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		session = write(request.Directory, generatetest.SessionID)
		document, err := generatetest.Document(request.Directory, nil)
		return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 50, SessionID: generatetest.SessionID}, err
	}
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	_, err = generate.Publish(ctx, s, key, o, nil)
	var failure *generate.Error
	if !errors.As(err, &failure) || failure.Category != generate.CategoryInterrupted {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("left in the root: %v", entries)
	}
	if session == "" || exists(session) || exists(filepath.Dir(session)) {
		t.Errorf("the CLI session %q stayed", session)
	}
	if _, err := s.Latest(context.Background(), key); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a version was stored: %v", err)
	}
}
