package generate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
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
	if info, err := os.Stat(session.Directory); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("kept directory: %v %v", info, err)
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
