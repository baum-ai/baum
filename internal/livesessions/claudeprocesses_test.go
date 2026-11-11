package livesessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeClaude is a ClaudeProcesses over a registry under home, with processes
// it makes up, that counts its registry reads and process confirmations.
type fakeClaude struct {
	*ClaudeProcesses
	home          string
	clock         *clock
	reads, probes int
	// started is when the process holding each running pid started.
	started map[int]string
}

func newFakeClaude(t *testing.T, home string) *fakeClaude {
	t.Helper()
	f := &fakeClaude{home: home, clock: &clock{t0}, started: map[int]string{}}
	f.ClaudeProcesses = &ClaudeProcesses{
		Home: home,
		now:  f.clock.Now,
		read: func(home string) (map[string][]ClaudeProcess, error) {
			f.reads++
			return readClaudeRegistry(home)
		},
		exists: func(pid int) bool { _, ok := f.started[pid]; return ok },
		same: func(p ClaudeProcess) error {
			f.probes++
			if f.started[p.PID] != p.ProcStart {
				return ErrAgentGone
			}
			return nil
		},
	}
	return f
}

// run starts a process pid at start, running session id, and registers it as
// Claude Code does.
func (f *fakeClaude) run(t *testing.T, pid int, id, status, start string) {
	t.Helper()
	f.started[pid] = start
	f.register(t, pid, id, status, start)
}

// register writes the registry entry of session id's process pid.
func (f *fakeClaude) register(t *testing.T, pid int, id, status, start string) {
	t.Helper()
	write(t, f.entry(pid), fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":%q,"procStart":%q}`, pid, id, status, start))
}

func (f *fakeClaude) entry(pid int) string {
	return filepath.Join(f.home, ".claude", "sessions", strconv.Itoa(pid)+".json")
}

func (f *fakeClaude) known() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.confirmed)
}

// A list of several waiting Claude Code sessions reads the registry once, and
// confirms each process once rather than on every list.
func TestTheListReadsClaudeCodesRegistryOncePerLook(t *testing.T) {
	f := newTranscriptFixture(t)
	claude := newFakeClaude(t, f.home)
	f.live.Enders = map[string]Ender{AgentClaudeCode: func(context.Context, string) error { return nil }}
	f.live.EndChecks = map[string]EndChecker{AgentClaudeCode: claude.EndCheck}
	ids := []string{"s1", "s2", "s3"}
	for i, id := range ids {
		if id != "s1" {
			path := filepath.Join(f.home, ".claude", "projects", "-work-demo", id+".jsonl")
			write(t, path, claudeTurn(id, 1, "Hello "+id, "Hi."))
			f.track(t, id, path)
		}
		claude.run(t, 100+i, id, "idle", "start-"+id)
	}
	// Working sessions are not offered End, so nothing is read.
	f.live.listed()
	if claude.reads != 0 {
		t.Fatalf("read the registry %d times for working sessions", claude.reads)
	}
	f.clock.advance(1)
	for _, id := range ids {
		e := event(id, "stop", f.clock.now, "/work/demo")
		e.Session.TranscriptPath = filepath.Join(f.home, ".claude", "projects", "-work-demo", id+".jsonl")
		apply(f.tracker, e)
	}
	for look := 1; look <= 3; look++ {
		endable := 0
		for _, l := range f.live.listed() {
			if l.Endable {
				endable++
			}
		}
		if endable != len(ids) || claude.reads != look || claude.probes != len(ids) {
			t.Fatalf("look %d: %d endable, %d reads, %d confirmations", look, endable, claude.reads, claude.probes)
		}
	}
}

func TestAConfirmedClaudeProcessIsConfirmedAgainOnlyWhenDueOrChanged(t *testing.T) {
	f := newFakeClaude(t, t.TempDir())
	const id = "75673aad-0000-4000-8000-000000000001"
	check := func() error { return f.EndCheck()(id) }
	f.run(t, 100, id, "idle", "S1")
	if err := check(); err != nil || f.probes != 1 {
		t.Fatalf("first look: %v, %d confirmations", err, f.probes)
	}
	if err := check(); err != nil || f.probes != 1 {
		t.Fatalf("unchanged: %v, %d confirmations", err, f.probes)
	}
	// Its status is read on every look; its process is not confirmed again.
	f.register(t, 100, id, "shell", "S1")
	if err := check(); !errors.Is(err, ErrRunningCommand) || f.probes != 1 {
		t.Fatalf("running a command: %v, %d confirmations", err, f.probes)
	}
	f.register(t, 100, id, "idle", "S1")
	// A process that exited is gone at once, and forgotten.
	delete(f.started, 100)
	if err := check(); !errors.Is(err, ErrAgentGone) || f.known() != 0 {
		t.Fatalf("exited: %v, %d known", err, f.known())
	}
	// Another process with its pid, under the entry the session left, is not
	// the session's.
	f.started[100] = "S9"
	if err := check(); !errors.Is(err, ErrAgentGone) || f.probes != 2 {
		t.Fatalf("a reused pid: %v, %d confirmations", err, f.probes)
	}
	// A new entry for the pid is confirmed anew.
	f.register(t, 100, id, "idle", "S9")
	if err := check(); err != nil || f.probes != 3 {
		t.Fatalf("a new entry: %v, %d confirmations", err, f.probes)
	}
	// And again once its confirmation is due.
	f.clock.advance(ReconfirmEvery - 1)
	if err := check(); err != nil || f.probes != 3 {
		t.Fatalf("not due: %v, %d confirmations", err, f.probes)
	}
	f.clock.advance(1)
	if err := check(); err != nil || f.probes != 4 {
		t.Fatalf("due: %v, %d confirmations", err, f.probes)
	}
	if !f.Alive(AgentClaudeCode, id) || f.Alive(AgentCodex, id) || f.probes != 4 {
		t.Fatalf("alive asked %d confirmations", f.probes)
	}
	// What was confirmed of a process whose entry is gone is forgotten.
	f.run(t, 101, "other", "idle", "S2")
	if err := f.EndCheck()("other"); err != nil || f.known() != 2 {
		t.Fatalf("other: %v, %d known", err, f.known())
	}
	if err := os.Remove(f.entry(101)); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil || f.known() != 1 {
		t.Fatalf("after its entry went: %v, %d known", err, f.known())
	}
}
