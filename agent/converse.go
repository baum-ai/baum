package agent

import (
	"context"
	"fmt"
)

// Access is what a conversation's agent may do on the person's machine
// without asking anyone: a conversation runs headless, so nothing it would ask
// for could be granted.
type Access string

const (
	// AccessAuto, the default, leaves what the agent may do without asking to
	// the agent's own reviewer: Claude Code's auto mode, whose classifier
	// approves or blocks each action, and Codex's workspace-write sandbox with
	// its automatic reviewer (approvals_reviewer auto_review) answering each
	// request to leave it. What the reviewer refuses is refused.
	AccessAuto Access = "auto"
	// AccessFull runs the agent as the person would with every prompt
	// approved: Claude Code's bypassPermissions mode, Codex without approvals
	// or sandbox.
	AccessFull Access = "full"
	// AccessWorkspace lets the agent edit files in its working directory and
	// nothing more without asking: Claude Code's acceptEdits mode, Codex's
	// workspace-write sandbox. What it would have to ask for is refused.
	AccessWorkspace Access = "workspace"
)

// Validate rejects an access level no backend knows.
func (a Access) Validate() error {
	if a != AccessAuto && a != AccessFull && a != AccessWorkspace {
		return fmt.Errorf("unknown access %q", string(a))
	}
	return nil
}

// Converser is a backend that runs one turn of a person's own agent session:
// the agent CLI as the person has set it up (their settings, hooks,
// instructions and MCP servers, plus Request.MCP next to them), in
// Request.Directory, with the access asked for. Request.Prompt is the
// person's message. The session is always kept, so Result.SessionID can be
// resumed with Request.ResumeSession for the next turn. The turn's final
// message, bounded to MaxConverseBytes, is Result.Document. Request.Schema,
// Task, ForkSession and the call and token limits are not used: the caller's
// context bounds the turn. Only the claude and codex backends implement it,
// in their local profiles.
type Converser interface {
	Converse(context.Context, Request, Access) (Result, error)
}

// MaxConverseBytes bounds the final message a Converser returns.
const MaxConverseBytes = 1 << 20
