package chat_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/chat/chattest"
	"github.com/baum-ai/baum/internal/reviewruns"
)

// A fork attempt that fails after creating a session and is retried leaves a
// rollout the turn's final session id does not name: the review-runs record
// must still delete it when the directory is pruned, without a scan.
func TestRetriedForkAttemptSessionIsRecorded(t *testing.T) {
	f := chattest.New(t, chattest.Options{Backend: agent.BackendCodex})
	if err := reviewruns.Record(f.Root, f.ReviewDir, true, f.SessionID); err != nil {
		t.Fatal(err)
	}
	failed := chattest.SessionID(900)
	f.Agent.Reply = func(r agent.Request, n int) (agent.Result, error) {
		if n == 0 {
			return agent.Result{SessionID: failed, Calls: 1}, errors.New("codex failed: provider overloaded")
		}
		return f.Agent.Default(r), nil
	}
	begun := f.Ask(t, "", "q")
	if _, err := run(t, f, begun, chat.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rollout := func(id string) string {
		name := "rollout-" + now.Format("2006-01-02T15-04-05") + "-" + id + ".jsonl"
		path := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), name)
		line := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}`+"\n", now.Format(time.RFC3339), id, f.ReviewDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	failedRollout, forkRollout := rollout(failed), rollout(chattest.SessionID(101))
	// The errored attempt may also have left sessions it never reported, so
	// the record is incomplete and a scan runs: this one is found by it.
	scanned := rollout(chattest.SessionID(777))
	if err := reviewruns.Remove(f.Root, f.ReviewDir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{failedRollout, forkRollout, scanned} {
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("%s stayed", path)
		}
	}
}
