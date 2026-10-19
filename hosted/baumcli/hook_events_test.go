//go:build !windows

package baumcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
)

// useTestHookDaemon points the hooks at a daemon location of the test's own,
// short enough for a socket path, and keeps them from starting a daemon.
func useTestHookDaemon(t *testing.T) daemon.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "bh")
	if err != nil {
		t.Fatal(err)
	}
	paths := daemon.Paths{State: filepath.Join(dir, "s"), Socket: filepath.Join(dir, "d.sock")}
	oldPaths, oldSpawn := hookDaemonPaths, hookDaemonSpawn
	hookDaemonPaths = func() (daemon.Paths, error) { return paths, nil }
	hookDaemonSpawn = func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
		return nil, errors.New("test: no daemon")
	}
	t.Cleanup(func() {
		hookDaemonPaths, hookDaemonSpawn = oldPaths, oldSpawn
		_ = os.RemoveAll(dir)
	})
	return paths
}

// spooledHookEvents reads the events waiting in the spool, with each file's
// raw bytes.
func spooledHookEvents(t *testing.T, paths daemon.Paths) ([]hookevents.Event, [][]byte) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(hookevents.SpoolDir(paths), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	var events []hookevents.Event
	var raw [][]byte
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var event hookevents.Event
		if err = json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		events, raw = append(events, event), append(raw, data)
	}
	return events, raw
}

// The agent's payload still reaches Entire byte for byte, and Entire's output
// and exit status still reach the agent; the event keeps only the session's
// identifiers, never the prompt or tool input the payload carries.
func TestAgentHookEventKeepsOnlySessionIdentifiers(t *testing.T) {
	paths := useTestHookDaemon(t)
	stubEntireBinary(t, "#!/bin/sh\ncat\nexit 3\n")
	payload := `{"session_id":"s-1","transcript_path":"/work/.claude/s-1.jsonl","cwd":"/work/demo","hook_event_name":"SessionEnd","reason":"prompt_input_exit",` +
		`"prompt":"use ` + fakeGitHubToken + `","tool_input":{"command":"cat ~/.netrc"}}`
	stdout, _, err := runCLIWithInput(t, payload, "hooks", "agent", "claude-code", "session-end")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("err = %v, want exit status 3", err)
	}
	if stdout != payload {
		t.Errorf("stdout = %q, want the payload Entire echoed", stdout)
	}
	events, raw := spooledHookEvents(t, paths)
	if len(events) != 1 {
		t.Fatalf("spooled %d events", len(events))
	}
	event := events[0]
	want := hookevents.Session{ID: "s-1", TranscriptPath: "/work/.claude/s-1.jsonl", Cwd: "/work/demo", HookEvent: "SessionEnd", Reason: "prompt_input_exit"}
	if event.Kind != hookevents.KindAgent || event.Agent != "claude-code" || event.Name != "session-end" || event.Exit != 3 ||
		event.V != hookevents.Version || event.Session == nil || *event.Session != want {
		t.Errorf("event = %+v, session %+v", event, event.Session)
	}
	if bytes.Contains(raw[0], []byte(fakeGitHubToken)) || bytes.Contains(raw[0], []byte("netrc")) {
		t.Errorf("the event kept payload content: %s", raw[0])
	}
}

// A pre-push hook's event names the remote but not its URL, which may carry
// credentials, and keeps the refs Git sends.
func TestGitHookEventLeavesOutTheRemoteURL(t *testing.T) {
	paths := useTestHookDaemon(t)
	received := filepath.Join(t.TempDir(), "stdin")
	stubEntireBinary(t, "#!/bin/sh\ncat > '"+received+"'\n")
	refs := "refs/heads/main 1111111111111111111111111111111111111111 refs/heads/main 2222222222222222222222222222222222222222\n"
	url := "https://ada:" + fakeGitHubToken + "@github.com/acme/demo.git"
	if _, _, err := runCLIWithInput(t, refs, "hooks", "git", "pre-push", "origin", url); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(received); string(data) != refs {
		t.Errorf("Entire read %q, want the refs", data)
	}
	events, raw := spooledHookEvents(t, paths)
	if len(events) != 1 {
		t.Fatalf("spooled %d events", len(events))
	}
	git := events[0].Git
	if events[0].Kind != hookevents.KindGit || events[0].Name != "pre-push" || git == nil ||
		!slices.Equal(git.Args, []string{"origin"}) || !slices.Equal(git.Lines, []string{strings.TrimSpace(refs)}) {
		t.Errorf("event = %+v, git %+v", events[0], git)
	}
	if bytes.Contains(raw[0], []byte(fakeGitHubToken)) {
		t.Error("the event kept the remote URL's credentials")
	}
}

// A daemon that takes the connection but never answers costs a hook at most
// hookevents.Budget; the event is spooled.
func TestHookReturnsWithinBudgetWhenTheDaemonHangs(t *testing.T) {
	paths := useTestHookDaemon(t)
	withoutEntire(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = conn.Read(make([]byte, 1<<16)) }()
		}
	}()
	start := time.Now()
	stdout, _, err := runCLIWithInput(t, `{"session_id":"s-2"}`, "hooks", "agent", "claude-code", "stop")
	elapsed := time.Since(start)
	if err != nil || stdout != "" {
		t.Fatalf("hook: %q, %v", stdout, err)
	}
	// The hook's own work besides the event takes a few milliseconds.
	if limit := hookevents.Budget + 100*time.Millisecond; elapsed > limit {
		t.Errorf("the hook took %s with a hung daemon, over %s", elapsed, limit)
	}
	if events, _ := spooledHookEvents(t, paths); len(events) != 1 || events[0].Session == nil || events[0].Session.ID != "s-2" {
		t.Errorf("spool = %+v", events)
	}
}

// The commands committed to customer repositories keep their contract with
// the agent and Git when no daemon can be reached: the same output and exit
// status, the event waiting in the spool, and nothing sent to a baum server.
func TestFrozenDispatchersKeepTheirContractWithoutADaemon(t *testing.T) {
	world := newHookWorld(t)
	t.Setenv(tokenEnvVar, "provisioned")
	entire, err := exec.LookPath("entire")
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, entire, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+world.entireLog+"'\ncat\nexit 3\n")
	paths, err := daemon.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}

	spec, _ := agenthooks.Lookup("claude-code")
	payload := `{"session_id":"s-3","cwd":` + quoteJSON(world.repo) + `}`
	cmd := exec.Command("sh", "-c", agenthooks.Dispatcher(spec, agenthooks.SessionEndEvent))
	cmd.Dir = world.repo
	cmd.Stdin = strings.NewReader(payload)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("dispatcher: %v, want exit status 3", err)
	}
	if stdout.String() != payload {
		t.Errorf("dispatcher stdout = %q, want the payload", stdout.String())
	}

	writeExecutable(t, entire, "#!/bin/sh\ncat >/dev/null\nexit 0\n")
	world.runGitDispatcher(t, "post-commit")

	events, _ := spooledHookEvents(t, paths)
	if len(events) != 2 {
		t.Fatalf("spooled %d events, want 2", len(events))
	}
	if events[0].Name != "session-end" || events[0].Exit != 3 || events[0].Session == nil || events[0].Session.ID != "s-3" {
		t.Errorf("agent event = %+v", events[0])
	}
	if events[1].Kind != hookevents.KindGit || events[1].Name != "post-commit" || events[1].Dir != world.repo {
		t.Errorf("git event = %+v", events[1])
	}
	if n := world.requests.Load(); n != 0 {
		t.Errorf("hooks sent %d requests to the baum server", n)
	}
}
