//go:build linux

package livesessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// procStartOf is pid's start as Claude Code records it on Linux.
func procStartOf(t *testing.T, pid int) string {
	t.Helper()
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))[19]
}

// register writes the ~/.claude/sessions entry of a session run by pid.
func register(t *testing.T, home string, pid int, id, status, procStart, domain string) {
	t.Helper()
	write(t, filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":%q,"procStart":%q,"pidDomain":%q,"kind":"interactive"}`, pid, id, status, procStart, domain))
}

func TestClaudeExitEndsAnIdleSessionsOwnProcessOnly(t *testing.T) {
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		t.Skip("no pid namespace to read")
	}
	domain := "linux::" + ns
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	t.Cleanup(func() { _ = child.Process.Kill() })
	pid := child.Process.Pid
	const id = "75673aad-0000-4000-8000-000000000001"
	home := t.TempDir()

	// An entry whose process started at another time is a reused pid.
	register(t, home, pid, id, "idle", "1", domain)
	if err := (ClaudeExit{Home: home}).End(context.Background(), id); !errors.Is(err, ErrAgentGone) {
		t.Fatalf("a reused pid: %v", err)
	}
	// Another pid namespace's: not one to signal, nor ended.
	register(t, home, pid, id, "idle", procStartOf(t, pid), "linux::pid:[1]")
	if err := (ClaudeExit{Home: home}).End(context.Background(), id); err == nil || errors.Is(err, ErrAgentGone) {
		t.Fatalf("another namespace: %v", err)
	}
	// Busy: left to finish its turn.
	register(t, home, pid, id, "busy", procStartOf(t, pid), domain)
	if err := (ClaudeExit{Home: home}).End(context.Background(), id); !errors.Is(err, errNotEndable) {
		t.Fatalf("a busy session: %v", err)
	}
	select {
	case <-done:
		t.Fatal("signalled before it was its own idle process")
	default:
	}
	register(t, home, pid, id, "idle", procStartOf(t, pid), domain)
	if err := (ClaudeExit{Home: home}).End(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "terminated") {
			t.Fatalf("exited with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not ended")
	}
}

func TestAPIDDomainIsThisMachinesNamespace(t *testing.T) {
	const ns = "pid:[4026531836]"
	for _, c := range []struct {
		domain, machine string
		want            bool
	}{
		{"linux:118ee7815fea41b2af87dd193bc7aa65:pid:[4026531836]", "118ee7815fea41b2af87dd193bc7aa65", true},
		// A container has no machine id.
		{"linux::pid:[4026531836]", "", true},
		{"linux::pid:[4026531836]", "118ee7815fea41b2af87dd193bc7aa65", true},
		{"linux:118ee7815fea41b2af87dd193bc7aa65:pid:[4026531836]", "", true},
		{"linux:118ee7815fea41b2af87dd193bc7aa65:pid:[4026534596]", "118ee7815fea41b2af87dd193bc7aa65", false},
		{"linux:aaaa:pid:[4026531836]", "118ee7815fea41b2af87dd193bc7aa65", false},
		{"darwin::pid:[4026531836]", "", false},
	} {
		if got := inDomain(c.domain, c.machine, ns); got != c.want {
			t.Errorf("inDomain(%q, %q) = %v", c.domain, c.machine, got)
		}
	}
}
