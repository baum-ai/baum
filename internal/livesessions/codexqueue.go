package livesessions

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CodexQueue sends to Codex sessions with Codex's own `codex queue --thread
// <id> --message <text>` (0.149 and later): the message arrives in the same
// thread as a turn the user typed, after the current one when the session is
// busy. A thread whose terminal was closed still runs it, in Codex's shared
// daemon, which is why the app offers it only after a finished turn.
type CodexQueue struct {
	// Codex is the codex command, looked up on PATH when the daemon starts.
	Codex string
}

// NewCodexQueue is a CodexQueue for the codex command on PATH, or false when
// there is none.
func NewCodexQueue() (CodexQueue, bool) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return CodexQueue{}, false
	}
	return CodexQueue{Codex: path}, true
}

// Send is a Sender.
func (c CodexQueue) Send(ctx context.Context, id, text string) error {
	if !safeID.MatchString(id) {
		return ErrAgentGone
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Codex, "queue", "--thread", id, "--message", text)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("codex queue for thread %s: %w: %s", id, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
