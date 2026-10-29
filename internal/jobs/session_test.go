package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
)

// Only a local Claude Code or Codex session can be resumed by a chat.
func TestOnlyClaudeAndCodexReviewsKeepTheirSession(t *testing.T) {
	for backend, want := range map[string]bool{
		agent.BackendClaude: true, agent.BackendCodex: true,
		agent.BackendCursorAgent: false, agent.BackendAnthropic: false, "": false,
	} {
		if got := keepsSession(backend); got != want {
			t.Errorf("keepsSession(%q) = %v, want %v", backend, got, want)
		}
	}
}

func reviewOptions(t *testing.T, backend *generatetest.Backend) generate.Options {
	t.Helper()
	repo := assembletest.NewRepo(t, assembletest.Options{})
	clone, err := gitlocal.Open(context.Background(), repo.Dir, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	return generate.Options{
		Assembler: &assemble.Assembler{
			GitHub: &assembletest.GitHub{Evidence: repo.Evidence()}, Git: clone,
			Sessions: &assembletest.Sessions{Found: assembletest.SessionTree()},
			Images:   func(context.Context, string, int64) ([]byte, error) { return assembletest.PNG, nil },
		},
		Request: assemble.Request{Repository: assembletest.Repository, Number: assembletest.Number, Limits: assemble.DefaultLimits()},
		Backend: backend, BackendVersion: "9.9.9",
		Budgets: agent.Budgets{WallClock: time.Minute, MaxCalls: 4, MaxTokens: 10000, MaxProviderAttempts: 1, MaxRepairAttempts: 2},
	}
}

func chmodTree(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}

// Jobs turn the option on for claude and codex, keep the run, and prune the
// runs of versions beyond the latest two once a new one is stored.
func TestPublishKeepsSessionsForClaudeAndCodexAndPrunesOldVersions(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, name := range []string{agent.BackendClaude, agent.BackendCodex, agent.BackendCursorAgent} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "review-runs")
			t.Cleanup(func() { chmodTree(root) })
			local := &Local{ReviewRuns: root}
			backend := &generatetest.Backend{BackendName: name, Version: "9.9.9"}
			versions, err := store.Open("", t.TempDir(), true)
			if err != nil {
				t.Fatal(err)
			}
			key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
			var dirs []string
			for range 3 {
				result, err := local.publish(ctx, versions, versions, key, name, reviewOptions(t, backend), nil)
				if err != nil {
					t.Fatal(err)
				}
				if name == agent.BackendCursorAgent {
					if result.Metadata.Session != nil {
						t.Fatalf("a %s review kept a session: %+v", name, result.Metadata.Session)
					}
					continue
				}
				if result.Metadata.Session == nil || result.Metadata.Session.Backend != name {
					t.Fatalf("session = %+v", result.Metadata.Session)
				}
				dirs = append(dirs, result.Metadata.Session.Directory)
			}
			entries, _ := os.ReadDir(root)
			if name == agent.BackendCursorAgent {
				if len(entries) != 0 || backend.Requests[0].PersistSession {
					t.Errorf("persist=%v, kept %d", backend.Requests[0].PersistSession, len(entries))
				}
				return
			}
			for i, want := range []bool{false, true, true} {
				if _, err := os.Stat(dirs[i]); (err == nil) != want {
					t.Errorf("version %d directory exists = %v, want %v", i+1, err == nil, want)
				}
			}
		})
	}
}
