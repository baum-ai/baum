// Package cursor runs the Cursor Agent CLI (cursor-agent) as an
// agent.Backend: a model call loop over a directory, in Cursor's read-only ask
// mode with its sandbox enabled, whose final message is a JSON document.
//
// cursor-agent has no structured output: the output schema travels in the
// prompt, and the generate-and-repair loop validates whatever comes back and
// sends the findings back as a repair. Its read-only guarantees are weaker than
// the other CLI backends'; ARCHITECTURE.md and SECURITY.md say what is and is
// not enforced.
package cursor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/proc"
)

const (
	// maxDocumentBytes bounds the document, as the other backends do.
	maxDocumentBytes = 8 << 20
	// maxLineBytes bounds one stream-json event. The result event repeats every
	// text the model wrote, so it is allowed twice the document.
	maxLineBytes = 2*maxDocumentBytes + 64<<10
	// maxEvents bounds the diagnostic events of one run.
	maxEvents = 256
)

// Backend runs the Cursor Agent CLI over a request's directory, under the
// user's own Cursor login. It has a single, local profile: the process gets an
// allowlisted copy of the environment (agent.LocalEnvironment, plus
// CURSOR_CONFIG_DIR, CURSOR_DATA_DIR and AGENT_CLI_CREDENTIAL_STORE), never a
// Cursor API key, and a request that names an Endpoint is refused.
//
// The child runs in its own process group, so a terminal's Ctrl-C (SIGINT) does
// not reach it. A local caller must cancel the context it passes to Run on
// SIGINT and SIGTERM, for example with signal.NotifyContext, or the CLI keeps
// running after baum exits. Cancelling kills the group on Unix, and on Windows
// only the child itself.
type Backend struct {
	// Binary is the executable. Empty looks up cursor-agent on PATH, then
	// agent, which is used only when its help says it is the Cursor Agent.
	Binary, Model string
	// Env are the user's agent variables (agent.WithUserEnv).
	Env []string

	mu      sync.Mutex
	checked bool
	failure error
	path    string
	version string
}

func (*Backend) Name() string { return agent.BackendCursorAgent }

// allowedTools are the cursor-agent tools a review may use: reading, listing
// and searching files, and its own to-do list. Any other tool the model starts
// (shell, edits, deletes, MCP, the web, subagents, mode switches, or a tool
// this list does not know) stops the run. This is detection, not prevention:
// the CLI reports a tool call when it starts it, so the call may have begun
// when the process group is killed.
var allowedTools = map[string]bool{
	"readToolCall": true, "grepToolCall": true, "globToolCall": true, "lsToolCall": true,
	"semSearchToolCall": true, "readLintsToolCall": true,
	"readTodosToolCall": true, "updateTodosToolCall": true,
}

// Run asks cursor-agent for the document. The prompt goes on stdin with the
// output schema appended, since the CLI cannot enforce one.
func (c *Backend) Run(ctx context.Context, request agent.Request) (result agent.Result, err error) {
	result.Model, result.ModelSource = c.Model, modelSource(c.Model)
	if request.WantsSession() {
		return result, errors.New("cursor-agent: session options are not supported")
	}
	if request.MaxCalls < 1 {
		return result, errors.New(agent.GenerationBudgetExceededReason("model calls"))
	}
	if request.MaxTokens < 1 {
		return result, errors.New(agent.GenerationBudgetExceededReason("tokens"))
	}
	if request.Endpoint != nil {
		return result, errors.New("cursor-agent: provider endpoints are not supported; cursor-agent runs under your own Cursor login")
	}
	binary, err := c.resolve(ctx)
	if err != nil {
		return result, err
	}
	// Without a usage report the zero buckets are unknown, not free.
	reported := false
	defer func() {
		if !reported {
			result.Usage.Unreported = true
		}
	}()

	started := time.Now()
	record := func(event agent.Event) {
		event.ElapsedMS = time.Since(started).Milliseconds()
		event.ID = bounded(event.ID, 128)
		event.Tool = bounded(event.Tool, 64)
		event.Status = bounded(event.Status, 64)
		if len(result.Events) == maxEvents {
			if event.Kind != "finished" {
				return
			}
			result.Events[maxEvents-1] = event
		} else {
			result.Events = append(result.Events, event)
		}
		if request.Logger != nil {
			request.Logger.Info("review agent event", "kind", event.Kind, "elapsed_ms", event.ElapsedMS, "id", event.ID, "tool", event.Tool, "path", event.Path, "bytes", event.Bytes, "status", event.Status, "input_tokens", event.Input, "output_tokens", event.Output, "cache_read_tokens", event.CacheRead, "cache_creation_tokens", event.CacheCreate)
		}
	}
	record(agent.Event{Kind: "started"})
	defer func() {
		status := "success"
		if err != nil {
			status = "failed"
		}
		record(agent.Event{Kind: "finished", Status: status})
	}()

	// Internal stops cancel runCtx rather than calling Cancel directly, so
	// WaitDelay bounds them as it does an outer cancellation.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(runCtx, binary, c.args(request.Directory)...)
	cmd.Dir = request.Directory
	cmd.Env = environment(c.Env)
	cmd.Stdin = strings.NewReader(promptWithSchema(request.Prompt, request.Schema))
	proc.Configure(cmd)
	// A command the agent started may hold stdout open after the group kill;
	// bound the wait for it.
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return result, err
	}

	// calls are the model calls the stream identifies. The final text of a run
	// has no model call ID; it counts as one more call when the run completes.
	calls := map[string]bool{}
	// final is the text of the final turn: every assistant event since the
	// last tool call, which the CLI may split across several events.
	var final strings.Builder
	finalUnidentified := false
	var resultText string
	completed := false
	// stopTool ends the run on a tool baum does not allow, or on an event it
	// does not know: detection is an allowlist and fails closed. Neither is
	// retried, since a pull request that steers the model into it would get
	// another chance on every attempt.
	stopTool := func(name, reason string) {
		record(agent.Event{Kind: "error", Tool: name, Status: "disallowed_tool"})
		err = reject("cursor-agent run stopped", "disallowed_tool", errors.New(reason))
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 65536), maxLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			// Every stream-json event is a JSON object; other output is not
			// an event.
			continue
		}
		var event streamEvent
		if json.Unmarshal(line, &event) != nil {
			stopTool("", "cursor-agent printed an event baum cannot read; the run was stopped")
			break
		}
		if reason := event.unknown(); reason != "" {
			stopTool("", "cursor-agent printed "+reason+", which baum does not allow in a review; the run was stopped")
			break
		}
		if event.ModelCallID != "" {
			calls[event.ModelCallID] = true
		}
		switch event.Type {
		case "system":
			if event.Subtype == "init" && c.Model == "" && event.Model != "" {
				result.Model = event.Model
			}
		case "user":
			// A new turn: earlier text is not its answer.
			final.Reset()
			finalUnidentified = false
		case "assistant":
			for _, content := range event.Message.Content {
				final.WriteString(content.Text)
			}
			if event.ModelCallID == "" {
				finalUnidentified = true
			}
			if final.Len() > maxDocumentBytes {
				err = errors.New("cursor-agent's final message exceeds the size limit")
			}
		case "tool_call":
			// Text before a tool call is not the final message.
			final.Reset()
			finalUnidentified = false
			name, path := event.tool(request.Directory)
			switch event.Subtype {
			case "started":
				record(agent.Event{Kind: "tool_call", ID: event.CallID, Tool: name, Path: path})
			case "completed":
				record(agent.Event{Kind: "tool_result", ID: event.CallID, Tool: name, Status: event.toolStatus()})
			}
			// Every tool event is checked, whatever its subtype.
			if !allowedTools[name] {
				stopTool(name, fmt.Sprintf("cursor-agent started the %s tool, which baum does not allow in a review; the run was stopped", orUnknown(name)))
			}
		case "retry":
			// A retried turn starts over: its text replaces the old text.
			final.Reset()
			finalUnidentified = false
			record(agent.Event{Kind: "retry", Status: event.Subtype})
		case "result":
			resultText = event.Result
			if event.Usage != nil {
				reported = true
				u := event.Usage
				// cursor-agent reports input with the cache reads and writes
				// already taken out, as Anthropic does.
				result.Usage = agent.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheCreate: u.CacheWrite}
				result.Tokens = agent.ChargedTokens(int(u.Input), int(u.Output), int(u.CacheWrite))
				record(agent.Event{Kind: "result", Status: event.Subtype, Input: int(u.Input), Output: int(u.Output), CacheRead: int(u.CacheRead), CacheCreate: int(u.CacheWrite)})
			} else {
				record(agent.Event{Kind: "result", Status: event.Subtype})
			}
			if event.IsError || event.Subtype != "success" {
				record(agent.Event{Kind: "error", Status: agent.ErrorStatus(event.Result)})
				err = errors.New(agent.ReasonProviderUnavailable)
			} else {
				completed = true
			}
		}
		result.Calls = len(calls)
		if completed && (len(calls) == 0 || (final.Len() > 0 && finalUnidentified)) {
			result.Calls++
		}
		if err == nil && result.Calls > request.MaxCalls {
			err = errors.New(agent.GenerationBudgetExceededReason("model calls"))
		}
		if err == nil && result.Tokens > request.MaxTokens {
			err = errors.New(agent.GenerationBudgetExceededReason("tokens"))
		}
		if err != nil {
			break
		}
	}
	if scanErr := scanner.Err(); err == nil && scanErr != nil {
		if errors.Is(scanErr, bufio.ErrTooLong) {
			scanErr = errors.New("cursor-agent output exceeds the size limit")
		}
		err = scanErr
	}
	if err != nil {
		// Wait closes stdout, so skip draining it: an escaped descendant may
		// hold it open indefinitely.
		stop()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		return result, err
	}
	if waitErr != nil {
		status, failure := runFailure(waitErr, stderr.String(), command(c.Binary, binary))
		record(agent.Event{Kind: "error", Status: status})
		return result, failure
	}
	if !completed {
		return result, errors.New("cursor-agent ended without a result")
	}
	// The final turn's text is the answer; the result's text, which repeats
	// everything the model wrote, is the fallback when that does not hold one
	// JSON value.
	document := extractDocument([]byte(final.String()))
	if !json.Valid(document) {
		if fallback := extractDocument([]byte(resultText)); json.Valid(fallback) || len(document) == 0 {
			document = fallback
		}
	}
	if len(document) == 0 || bytes.Equal(document, []byte("null")) {
		return result, errors.New("cursor-agent returned no review document")
	}
	if len(document) > maxDocumentBytes {
		return result, errors.New("agent document exceeds output size limit")
	}
	result.Document = document
	record(agent.Event{Kind: "document", Bytes: len(document)})
	return result, nil
}

// args are the flags of every run. The prompt is not among them: it goes on
// stdin, which cursor-agent reads when no prompt argument is given, so its size
// is not bounded by the command line.
func (c *Backend) args(directory string) []string {
	args := []string{"--print", "--output-format", "stream-json",
		// Ask mode is Cursor's read-only mode; a headless run cannot switch
		// out of it. The sandbox confines any command that still runs to the
		// workspace without network access, and a run where the sandbox
		// cannot start fails instead of running unsandboxed.
		"--mode", "ask", "--sandbox", "enabled",
		// The run directory is the only workspace. --trust answers the
		// workspace trust prompt, which a headless run cannot; it does not
		// approve commands (--force would).
		"--workspace", directory, "--trust"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return args
}

// environmentNames are the variables, besides agent.LocalEnvironment's
// allowlist, that locate the user's own Cursor configuration and login.
var environmentNames = []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "AGENT_CLI_CREDENTIAL_STORE"}

// environment is the process environment, which is never the caller's
// wholesale. NO_OPEN_BROWSER stops a run that is not signed in from opening a
// login page.
func environment(user []string) []string {
	return append(agent.WithUserEnv(agent.LocalEnvironment(os.Environ(), environmentNames...), user), "NO_OPEN_BROWSER=1")
}

// schemaHeading introduces the output schema at the end of every prompt.
const schemaHeading = "\n\nOutput schema (JSON Schema). Your final message must be one JSON object that matches it, with nothing before or after it:\n"

// promptWithSchema appends the output schema to the prompt: cursor-agent has
// no structured output to carry it.
func promptWithSchema(prompt string, schema []byte) string {
	if len(schema) == 0 {
		return prompt
	}
	return prompt + schemaHeading + string(schema) + "\n"
}

// extractDocument extracts the review document from the model's final message. A
// message that is one JSON object is kept as it is; one wrapped in a Markdown
// fence, or with text around a single object, yields that object. Anything else
// is returned trimmed, so the validator rejects it and the loop asks for a
// repair: the output is never taken on trust because the CLI cannot enforce a
// schema.
func extractDocument(message []byte) []byte {
	text := bytes.TrimSpace(message)
	if len(text) == 0 || json.Valid(text) {
		return text
	}
	if unfenced, ok := unfence(text); ok && json.Valid(unfenced) {
		return unfenced
	}
	start, end := bytes.IndexByte(text, '{'), bytes.LastIndexByte(text, '}')
	if start >= 0 && end > start && json.Valid(text[start:end+1]) {
		return text[start : end+1]
	}
	return text
}

// unfence returns the body of a message that is a single fenced code block.
func unfence(text []byte) ([]byte, bool) {
	if !bytes.HasPrefix(text, []byte("```")) || !bytes.HasSuffix(text, []byte("```")) || len(text) < 6 {
		return nil, false
	}
	body := text[3 : len(text)-3]
	newline := bytes.IndexByte(body, '\n')
	if newline < 0 {
		return nil, false
	}
	return bytes.TrimSpace(body[newline+1:]), true
}

// streamEvent is one line of cursor-agent's stream-json output. Only the fields
// baum reads are decoded.
type streamEvent struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Model       string `json:"model"`
	ModelCallID string `json:"model_call_id"`
	CallID      string `json:"call_id"`
	QueryType   string `json:"query_type"`
	IsError     bool   `json:"is_error"`
	Result      string `json:"result"`
	Message     struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	ToolCall map[string]json.RawMessage `json:"tool_call"`
	Usage    *struct {
		Input      int64 `json:"inputTokens"`
		Output     int64 `json:"outputTokens"`
		CacheRead  int64 `json:"cacheReadTokens"`
		CacheWrite int64 `json:"cacheWriteTokens"`
	} `json:"usage"`
}

// knownEvents are the stream-json event types and subtypes cursor-agent
// 2026.10.16 writes in a run that only reads, as read from its source; an
// empty list allows any subtype. task_notification and background_shell_timeout
// report shells or subagents, and interaction_query requests (web search and
// fetch, mode switches), so they are not on it; askQuestion is the exception,
// which a headless run declines by itself.
var knownEvents = map[string][]string{
	"system": {"init"}, "user": nil, "assistant": nil, "tool_call": {"started", "completed"},
	"thinking": nil, "result": nil, "retry": nil, "connection": nil,
	"interaction_query": {"request", "response"},
}

// unknown says what about an event is not on the allowlist, empty when all of
// it is: its type and subtype, an interaction other than a question, a
// content block that is not text, or a tool call outside a tool_call event.
func (e streamEvent) unknown() string {
	subtypes, known := knownEvents[e.Type]
	switch {
	case !known:
		return fmt.Sprintf("a %q event", bounded(e.Type, 64))
	case len(subtypes) > 0 && !slices.Contains(subtypes, e.Subtype):
		return fmt.Sprintf("a %q %s event", bounded(e.Subtype, 64), e.Type)
	case e.Type == "interaction_query" && e.QueryType != "askQuestionInteractionQuery":
		return fmt.Sprintf("a %q interaction", bounded(e.QueryType, 64))
	case e.ToolCall != nil && e.Type != "tool_call":
		return "a tool call in a " + e.Type + " event"
	}
	for _, content := range e.Message.Content {
		if content.Type != "text" {
			return fmt.Sprintf("a %q content block", bounded(content.Type, 64))
		}
	}
	return ""
}

// tool returns the tool a tool_call event names, the single key of its
// tool_call object, and the path it reads, relative to the run directory.
func (e streamEvent) tool(directory string) (name, path string) {
	if len(e.ToolCall) != 1 {
		return "", ""
	}
	for key, body := range e.ToolCall {
		var call struct {
			Args struct {
				Path string `json:"path"`
			} `json:"args"`
		}
		_ = json.Unmarshal(body, &call)
		name, path = key, agent.EventPath(directory, call.Args.Path)
	}
	return name, path
}

// toolStatus is the outcome of a completed tool call: the key of its result
// object (success, error, rejected, ...), or "unknown".
func (e streamEvent) toolStatus() string {
	for _, body := range e.ToolCall {
		var call struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		if json.Unmarshal(body, &call) == nil && len(call.Result) == 1 {
			for status := range call.Result {
				return status
			}
		}
	}
	return "unknown"
}

// modelSource says where Result.Model came from: the configured model, or the
// display name cursor-agent reports for its own default. Neither is a provider
// model ID, so a run's cost stays unknown (Cursor bills through the user's
// subscription and reports no speed or cache TTL).
func modelSource(model string) string {
	if model != "" {
		return "configured"
	}
	return "cli_reported"
}

func orUnknown(name string) string {
	if name == "" {
		return "unknown"
	}
	return name
}

func bounded(s string, limit int) string { return s[:min(len(s), limit)] }

// limitedBuffer keeps the first 64 KiB of diagnostic output.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 65536 {
		_, _ = b.Buffer.Write(p[:min(len(p), 65536-b.Len())])
	}
	return n, nil
}
