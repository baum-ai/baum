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

// completeMaxTurns bounds a completion: one turn answers it.
const completeMaxTurns = 2

// Complete runs `codex exec` once for a single structured reply
// (agent.Completer): ephemeral, without the user's config, rules or hooks, in
// its read-only sandbox, with the shell and other tools turned off, low
// reasoning effort, the request's system prompt as the model instructions and
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
		"-c", "model_instructions_file=" + string(path), "-c", `model_reasoning_effort="low"`}
	run := agent.Request{Directory: work, Prompt: sessions.GenerationRunMarker + "\n" + request.User, Schema: request.Schema,
		MaxCalls: completeMaxTurns, MaxTokens: 1 << 30}
	result, err := c.run(ctx, run, false, extra)
	return agent.Completion{Document: result.Document, Model: result.Model, Usage: result.Usage}, err
}
