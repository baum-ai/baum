package claude

import (
	"cmp"
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/sessions"
)

// CompleteMaxTurns bounds a completion's model calls. Claude Code ends the
// run once a StructuredOutput call is accepted, so a completion normally
// makes one call; the second turn lets the model submit once more after the
// harness rejected its reply against the schema, and no further retry is
// made.
const CompleteMaxTurns = 2

// Complete runs Claude Code once with no tools but the StructuredOutput tool
// --json-schema registers (agent.Completer): the request's system prompt, no
// settings, plugins, MCP servers or hooks, no session persisted, in an empty
// temporary directory. The prompt goes on stdin and starts with
// sessions.GenerationRunMarker, so session discovery leaves the run out should
// it be recorded after all. Only the User profile completes.
func (c *Backend) Complete(ctx context.Context, request agent.CompleteRequest) (agent.Completion, error) {
	if c.Profile != User {
		return agent.Completion{}, errors.New("claude: only the User profile completes")
	}
	dir, err := os.MkdirTemp("", agent.RunDirPrefix+"*")
	if err != nil {
		return agent.Completion{}, err
	}
	defer os.RemoveAll(dir)
	run := agent.Request{Directory: dir, Prompt: sessions.GenerationRunMarker + "\n" + request.User, Schema: request.Schema,
		MaxCalls: CompleteMaxTurns, MaxTokens: 1 << 30}
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, c.effortArgs()...)
	args = append(args, "--system-prompt", request.System+"\n\n"+submitPrompt, "--max-turns", strconv.Itoa(CompleteMaxTurns),
		"--restricted", "--tools", "", "--json-schema", string(request.Schema), "--no-session-persistence",
		"--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	env := []string{"CLAUDE_CODE_MAX_OUTPUT_TOKENS=" + strconv.Itoa(cmp.Or(request.MaxOutputTokens, maxOutputTokens))}
	result, err := c.run(ctx, run, args, env, false)
	return agent.Completion{Document: result.Document, Model: cmp.Or(result.Model, c.Model), Usage: result.Usage,
		Rejections: result.StructuredOutputRejections, RejectionDetail: result.LastRejection}, err
}
