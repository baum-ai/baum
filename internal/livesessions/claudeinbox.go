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

	"github.com/baum-ai/baum/internal/agentdirs"
)

// ClaudeProcess is a running Claude Code session as Claude Code registers it
// in ~/.claude/sessions/<pid>.json.
type ClaudeProcess struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	// Status is "idle", "busy", or "shell" while its turn is over but a
	// background command it started still runs.
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
	byID, err := readClaudeRegistry(home)
	if err != nil {
		return ClaudeProcess{}, err
	}
	return pickClaude(byID[id], func(p ClaudeProcess) error {
		if !running(p.PID) {
			return ErrAgentGone
		}
		return sameProcess(p)
	})
}

// readClaudeRegistry is the entries under the sessions folder of each Claude
// Code configuration directory baum knows (agentdirs.Claude.All: ~/.claude under
// home, then the others) that name a session and a pid, by session id. An entry it cannot read is passed over.
func readClaudeRegistry(home string) (map[string][]ClaudeProcess, error) {
	var entries []string
	for _, dir := range agentdirs.Claude.All(home) {
		found, err := filepath.Glob(filepath.Join(dir, "sessions", "*.json"))
		if err != nil {
			return nil, err
		}
		entries = append(entries, found...)
	}
	byID := make(map[string][]ClaudeProcess, len(entries))
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p ClaudeProcess
		if json.Unmarshal(data, &p) != nil || p.SessionID == "" || p.PID <= 0 {
			continue
		}
		byID[p.SessionID] = append(byID[p.SessionID], p)
	}
	return byID, nil
}

// pickClaude is the first of a session's entries whose process confirm
// confirms, else ErrAgentGone when each one's is gone, else why one could
// not be confirmed.
func pickClaude(entries []ClaudeProcess, confirm func(ClaudeProcess) error) (ClaudeProcess, error) {
	unconfirmed := ErrAgentGone
	for _, p := range entries {
		switch err := confirm(p); {
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
// signalled: a busy one would lose the turn it is in, and one whose turn is
// over but that still runs a background command (status "shell") would lose
// the command (ErrRunningCommand).
type ClaudeExit struct {
	// Home holds ~/.claude; empty is the user's home.
	Home string
}

// End is an Ender.
func (c ClaudeExit) End(_ context.Context, id string) error {
	p, err := c.idle(id)
	if err != nil {
		return err
	}
	return terminate(p.PID)
}

// Check is an EndCheck: whether End would signal the session now.
func (c ClaudeExit) Check(id string) error {
	_, err := c.idle(id)
	return err
}

// idle is the process of session id when End may signal it, or why not.
func (c ClaudeExit) idle(id string) (ClaudeProcess, error) {
	home, err := orHome(c.Home)
	if err != nil {
		return ClaudeProcess{}, err
	}
	return idleClaude(FindClaude(home, id))
}

// idleClaude is p, found with err, when End may signal it, or why not.
func idleClaude(p ClaudeProcess, err error) (ClaudeProcess, error) {
	if err != nil {
		return ClaudeProcess{}, err
	}
	switch p.Status {
	case "idle":
		return p, nil
	case "shell":
		return ClaudeProcess{}, ErrRunningCommand
	}
	return ClaudeProcess{}, errNotEndable
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
