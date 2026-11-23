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
)

// otherClaudeDir makes a second Claude Code configuration directory beside
// the fixture's ~/.claude, with the transcript of session id in its store,
// and gives the test its own, empty record of directories.
func otherClaudeDir(t *testing.T, f *transcriptFixture, id string) (dir, path string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	dir = filepath.Join(f.home, ".claude.personal")
	path = filepath.Join(dir, "projects", "-work-demo", id+".jsonl")
	write(t, path, claudeTurn(id, 1, "From the other account", "Done."))
	return dir, path
}

// A session run with another configuration directory keeps its transcript
// there. It is read once baum knows the directory, and refused before.
func TestTranscriptIsReadFromARecordedClaudeConfigDirectory(t *testing.T) {
	f := newTranscriptFixture(t)
	dir, path := otherClaudeDir(t, f, "p1")
	f.track(t, "p1", path)
	var missing *Unavailable
	if _, err := f.live.Get(context.Background(), "claude-code", "p1"); !errors.As(err, &missing) || !strings.Contains(missing.Reason, "outside") {
		t.Fatalf("a directory baum does not know: %v", err)
	}
	if _, err := agentdirs.Claude.Remember(dir); err != nil {
		t.Fatal(err)
	}
	f.clock.now = f.clock.now.Add(2 * RecheckEvery)
	code, body, raw := get(t, f.live.Handler(), "/api/live-sessions/claude-code/p1/transcript")
	if code != http.StatusOK || !body.Available || body.Transcript == nil || len(body.Transcript.Session.Prompts) != 1 || body.Transcript.Session.Prompts[0].Text != "From the other account" {
		t.Fatalf("%d %s", code, raw)
	}
	// The default store is still read, and a path outside both still is not.
	if code, body, raw := get(t, f.live.Handler(), "/api/live-sessions/claude-code/s1/transcript"); code != http.StatusOK || !body.Available {
		t.Fatalf("the default store: %d %s", code, raw)
	}
	outside := filepath.Join(f.home, "elsewhere", "p2.jsonl")
	write(t, outside, claudeTurn("p2", 1, "private", "reply"))
	f.track(t, "p2", outside)
	if _, err := f.live.Get(context.Background(), "claude-code", "p2"); !errors.As(err, &missing) || !strings.Contains(missing.Reason, "outside") {
		t.Fatalf("outside every store: %v", err)
	}
}

// The daemon's own CLAUDE_CONFIG_DIR is a store too, and a session whose
// hooks named no transcript is found in it by its id.
func TestTranscriptIsFoundByIDInTheEnvironmentsClaudeConfigDirectory(t *testing.T) {
	f := newTranscriptFixture(t)
	dir, path := otherClaudeDir(t, f, "p1")
	t.Setenv(agentdirs.Claude.Env, dir)
	f.track(t, "p1", "")
	loc, err := Locate(f.home, "", AgentClaudeCode, "p1", "")
	if err != nil || loc.main != path {
		t.Fatalf("Locate = %+v, %v", loc, err)
	}
	if code, body, raw := get(t, f.live.Handler(), "/api/live-sessions/claude-code/p1/transcript"); code != http.StatusOK || !body.Available {
		t.Fatalf("%d %s", code, raw)
	}
}

// A recorded directory that is gone does not hide the others, nor change why
// a path is refused.
func TestAMissingClaudeConfigDirectoryIsPassedOver(t *testing.T) {
	f := newTranscriptFixture(t)
	dir, _ := otherClaudeDir(t, f, "p1")
	if _, err := agentdirs.Claude.Remember(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Locate(f.home, "", AgentClaudeCode, "s1", f.path); err != nil {
		t.Fatalf("the default store: %v", err)
	}
	var missing *Unavailable
	gone := filepath.Join(f.home, ".claude", "projects", "-work-demo", "gone.jsonl")
	if _, err := Locate(f.home, "", AgentClaudeCode, "gone", gone); !errors.As(err, &missing) || !strings.Contains(missing.Reason, "not there any more") {
		t.Fatalf("a missing transcript: %v", err)
	}
}

// Claude Code registers a running session under its own configuration
// directory, so the process of one run with another directory is found there.
func TestClaudeRegistryIsReadFromEveryKnownConfigDirectory(t *testing.T) {
	f := newTranscriptFixture(t)
	dir, _ := otherClaudeDir(t, f, "p1")
	write(t, filepath.Join(f.home, ".claude", "sessions", "11.json"), `{"pid":11,"sessionId":"s1","status":"idle"}`)
	write(t, filepath.Join(dir, "sessions", "22.json"), `{"pid":22,"sessionId":"p1","status":"busy"}`)
	byID, err := readClaudeRegistry(f.home)
	if err != nil || len(byID["s1"]) != 1 || len(byID["p1"]) != 0 {
		t.Fatalf("before the directory is known: %v, %v", byID, err)
	}
	if _, err := agentdirs.Claude.Remember(dir); err != nil {
		t.Fatal(err)
	}
	byID, err = readClaudeRegistry(f.home)
	if err != nil || len(byID["s1"]) != 1 || len(byID["p1"]) != 1 || byID["p1"][0].PID != 22 {
		t.Fatalf("readClaudeRegistry = %v, %v", byID, err)
	}
}
