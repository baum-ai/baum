package claude

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
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
// it be recorded after all. A request with images sends the prompt as one
// stream-json user message whose image blocks follow the text. Only the User
// profile completes.
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
	if len(request.Images) > 0 {
		if run.Prompt, err = imageMessage(run.Prompt, request.Images); err != nil {
			return agent.Completion{}, err
		}
		args = append(args, "--input-format", "stream-json")
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, c.effortArgs()...)
	args = append(args, "--system-prompt", request.System+"\n\n"+submitPrompt, "--max-turns", strconv.Itoa(CompleteMaxTurns),
		"--restricted", "--tools", "", "--json-schema", string(request.Schema), "--no-session-persistence",
		"--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	var env []string
	if request.MaxOutputTokens > 0 {
		env = append(env, "CLAUDE_CODE_MAX_OUTPUT_TOKENS="+strconv.Itoa(request.MaxOutputTokens))
	}
	result, err := c.run(ctx, run, args, env, runReview)
	return agent.Completion{Document: result.Document, Model: cmp.Or(result.Model, c.Model), Usage: result.Usage,
		Rejections: result.StructuredOutputRejections, RejectionDetail: result.LastRejection}, err
}

// imageMessage is the stream-json line of a user message holding text and
// then each image, base64 encoded.
func imageMessage(text string, images []agent.CompleteImage) (string, error) {
	type source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type block struct {
		Type   string  `json:"type"`
		Text   string  `json:"text,omitempty"`
		Source *source `json:"source,omitempty"`
	}
	content := []block{{Type: "text", Text: text}}
	for _, image := range images {
		raw, err := os.ReadFile(image.Path)
		if err != nil {
			return "", err
		}
		content = append(content, block{Type: "image", Source: &source{Type: "base64", MediaType: image.MediaType, Data: base64.StdEncoding.EncodeToString(raw)}})
	}
	line, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	return string(line) + "\n", err
}
