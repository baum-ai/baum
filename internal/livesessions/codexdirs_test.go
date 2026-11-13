package livesessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/hookevents"
)

const otherCodexID = "019f0000-0000-7000-8000-0000000000c1"

// otherCodexHome makes a second Codex home beside the fixture's ~/.codex,
// with the rollout of otherCodexID in its sessions folder, and gives the test
// its own, empty record of homes.
func otherCodexHome(t *testing.T, f *transcriptFixture) (dir, path string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.home, ".config"))
	t.Setenv(agentdirs.Codex.Env, "")
	if err := os.MkdirAll(filepath.Join(f.home, ".codex", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(f.home, ".codex-work")
	path = filepath.Join(dir, "sessions", "2026", "10", "20", "rollout-2026-10-20T09-00-00-"+otherCodexID+".jsonl")
	write(t, path, `{"timestamp":"2026-10-20T09:00:00.000Z","type":"session_meta","payload":{"id":"`+otherCodexID+`","cwd":"/work/demo","cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-10-20T09:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"From the work account"}}
`)
	return dir, path
}

func (f *transcriptFixture) trackCodex(t *testing.T, id, path string) {
	t.Helper()
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo", TranscriptPath: path}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
}

// A session run with another Codex home keeps its rollout there. It is read
// once baum knows the home, and refused before.
func TestCodexTranscriptIsReadFromARecordedHome(t *testing.T) {
	f := newTranscriptFixture(t)
	dir, path := otherCodexHome(t, f)
	f.trackCodex(t, otherCodexID, path)
	var missing *Unavailable
	if _, err := f.live.Get(context.Background(), "codex", otherCodexID); !errors.As(err, &missing) || !strings.Contains(missing.Reason, "outside") {
		t.Fatalf("a home baum does not know: %v", err)
	}
	if _, err := agentdirs.Codex.Remember(dir); err != nil {
		t.Fatal(err)
	}
	f.clock.now = f.clock.now.Add(2 * RecheckEvery)
	answer, err := f.live.Get(context.Background(), "codex", otherCodexID)
	if err != nil || !strings.Contains(string(answer.Body), "From the work account") {
		t.Fatalf("%v %s", err, answer.Body)
	}
	// A Claude Code configuration directory is not a Codex store.
	if _, err := agentdirs.Claude.Remember(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := agentdirs.Codex.Forget(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Locate(f.home, "", AgentCodex, otherCodexID, path); !errors.As(err, &missing) || !strings.Contains(missing.Reason, "outside") {
		t.Fatalf("read through Claude's record: %v", err)
	}
}

// The daemon's own CODEX_HOME is a store too: a session whose hooks named no
// rollout is found there by its id, and its title comes from that home's
// session index.
func TestCodexSessionIsFoundByIDAndNamedInTheEnvironmentsHome(t *testing.T) {
	f := newTranscriptFixture(t)
	dir, path := otherCodexHome(t, f)
	t.Setenv(agentdirs.Codex.Env, dir)
	loc, err := Locate(f.home, "", AgentCodex, otherCodexID, "")
	if err != nil || loc.main != path {
		t.Fatalf("Locate = %+v, %v", loc, err)
	}
	f.trackCodex(t, otherCodexID, "")
	write(t, filepath.Join(dir, "session_index.jsonl"), `{"id":"`+otherCodexID+`","thread_name":"Work thread","updated_at":"2026-10-20T09:00:06Z"}
`)
	if got := titles(t, f)["codex/"+otherCodexID]; got != "Work thread" {
		t.Fatalf("title %q", got)
	}
	if code, body, raw := get(t, f.live.Handler(), "/api/live-sessions/codex/"+otherCodexID+"/transcript"); code != http.StatusOK || !body.Available {
		t.Fatalf("%d %s", code, raw)
	}
}
