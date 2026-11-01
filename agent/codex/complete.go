package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/sessions"
)

// completeMaxTurns bounds a completion to one turn. With the shell tools off a
// turn is normally one model request, but a turn that calls a tool Codex
// still offers (its patch or plan tools, which the read-only sandbox and an
// empty directory leave nothing to act on) makes another request within it.
// The CLI has no option capping the reply's tokens.
const completeMaxTurns = 1

// Complete runs `codex exec` once for a single structured reply
// (agent.Completer): ephemeral, without the user's config, rules or hooks, in
// its read-only sandbox, with the shell and other tools turned off, the
// Backend's reasoning effort (Codex's own when empty), the request's system prompt as the model instructions and
// the request's schema as the output schema, in an empty temporary directory.
// The prompt goes on stdin and starts with sessions.GenerationRunMarker. Only
// the Local profile completes.
func (c *Backend) Complete(ctx context.Context, request agent.CompleteRequest) (agent.Completion, error) {
	if c.Profile != Local {
		return agent.Completion{}, errors.New("codex: only the Local profile completes")
	}
	dir, err := os.MkdirTemp("", agent.RunDirPrefix+"*")
	if err != nil {
		return agent.Completion{}, err
	}
	defer os.RemoveAll(dir)
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		return agent.Completion{}, err
	}
	instructions := filepath.Join(dir, "instructions.md")
	if err := os.WriteFile(instructions, []byte(request.System), 0o600); err != nil {
		return agent.Completion{}, err
	}
	path, err := json.Marshal(instructions)
	if err != nil {
		return agent.Completion{}, err
	}
	extra := []string{"--disable", "shell_tool", "--disable", "unified_exec",
		"-c", "model_instructions_file=" + string(path)}
	run := agent.Request{Directory: work, Prompt: sessions.GenerationRunMarker + "\n" + request.User, Schema: request.Schema,
		MaxCalls: completeMaxTurns, MaxTokens: 1 << 30}
	result, err := c.run(ctx, run, false, extra)
	return agent.Completion{Document: result.Document, Model: result.Model, Usage: result.Usage}, err
}
