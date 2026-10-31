package livesessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ClaudeProcess is a running Claude Code session as Claude Code registers it
// in ~/.claude/sessions/<pid>.json.
type ClaudeProcess struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	// Status is "idle" or "busy".
	Status string `json:"status"`
	// Socket is the session's cross-session inbox, a Unix socket.
	Socket string `json:"messagingSocketPath"`
	Kind   string `json:"kind"`
	// ProcStart is when the process started as its system counts it (on
	// Linux, field 22 of /proc/<pid>/stat), and PIDDomain the pid namespace
	// the pid is in ("linux::pid:[…]"): Claude Code records both, so a pid a
	// later process reused, under an entry a killed session left, is told
	// apart from the session's own.
	ProcStart string `json:"procStart"`
	PIDDomain string `json:"pidDomain"`
}

// FindClaude is the running process of Claude Code session id under home's
// ~/.claude/sessions, or ErrAgentGone: an entry naming the session whose
// process still runs, and is the one that wrote it (sameProcess). Claude Code
// removes an entry when its session exits; one a killed process left is
// passed over. A process baum cannot confirm is an error, not ErrAgentGone,
// so it is neither signalled nor recorded ended.
func FindClaude(home, id string) (ClaudeProcess, error) {
	if !safeID.MatchString(id) {
		return ClaudeProcess{}, ErrAgentGone
	}
	entries, err := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	if err != nil {
		return ClaudeProcess{}, err
	}
	unconfirmed := ErrAgentGone
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p ClaudeProcess
		if json.Unmarshal(data, &p) != nil || p.SessionID != id || p.PID <= 0 || !running(p.PID) {
			continue
		}
		switch err := sameProcess(p); {
		case err == nil:
			return p, nil
		case !errors.Is(err, ErrAgentGone):
			unconfirmed = err
		}
	}
	return ClaudeProcess{}, unconfirmed
}

// ClaudeExit ends Claude Code sessions as /exit does: SIGTERM to the
// session's process, which then exits in under a second, leaves its terminal
// at the shell, fires its SessionEnd hook (reason "other") and can be resumed
// with `claude --resume <id>`. Only a process that reports itself idle is
// signalled: a busy one would lose the turn it is in.
type ClaudeExit struct {
	// Home holds ~/.claude; empty is the user's home.
	Home string
}

// End is an Ender.
func (c ClaudeExit) End(_ context.Context, id string) error {
	home, err := orHome(c.Home)
	if err != nil {
		return err
	}
	p, err := FindClaude(home, id)
	if err != nil {
		return err
	}
	if p.Status != "idle" {
		return errNotEndable
	}
	return terminate(p.PID)
}

// ClaudeAlive is a Tracker's Alive: whether the process of a Claude Code
// session still runs under home's ~/.claude (empty is the user's home), as
// FindClaude confirms it. Other agents register no process, so theirs are
// never confirmed.
type ClaudeAlive struct {
	Home string
}

// Alive is a Tracker's Alive.
func (c ClaudeAlive) Alive(agent, id string) bool {
	if agent != AgentClaudeCode {
		return false
	}
	home, err := orHome(c.Home)
	if err != nil {
		return false
	}
	_, err = FindClaude(home, id)
	return err == nil
}

func orHome(home string) (string, error) {
	if home != "" {
		return home, nil
	}
	return os.UserHomeDir()
}

// ClaudeInbox sends to Claude Code sessions through their cross-session inbox
// (https://code.claude.com/docs/en/cross-session-messaging): one JSON line
// on the session's socket, which an idle session takes as a new turn, in its
// own terminal and transcript. A session's crossSessionInbound setting can
// hold or refuse it. On macOS and Linux only: on Windows the inbox is a named
// pipe.
type ClaudeInbox struct {
	// Home holds ~/.claude; empty is the user's home.
	Home string
}

// Send is a Sender.
func (c ClaudeInbox) Send(ctx context.Context, id, text string) error {
	home, err := orHome(c.Home)
	if err != nil {
		return err
	}
	p, err := FindClaude(home, id)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(p.Socket) {
		return fmt.Errorf("claude code session %s has no inbox", id)
	}
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"from":    "baum",
		"message": map[string]string{"role": "user", "content": framed(text)},
	})
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", p.Socket)
	if err != nil {
		if !running(p.PID) {
			return ErrAgentGone
		}
		return fmt.Errorf("reaching claude code session %s: %w", id, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("messaging claude code session %s: %w", id, err)
	}
	return nil
}
