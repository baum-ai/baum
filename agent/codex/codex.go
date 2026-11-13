package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/proc"
)

// Profile selects the sandbox, environment and credentials the Codex process gets.
type Profile int

const (
	// Local is for local runs and the zero value. Codex runs in its read-only
	// sandbox: commands it starts cannot write or reach the network, but they
	// can read any file the user can, not only the working directory. The
	// process gets the user's own HOME and CODEX_HOME (or ~/.codex), so
	// `codex login` auth works, an allowlisted copy of the environment
	// (agent.LocalEnvironment, plus CODEX_HOME) and the binary found on PATH.
	// CODEX_API_KEY and the base URL are set only when the request names an
	// Endpoint. The output schema goes to a private temporary file removed
	// after the run, never into CODEX_HOME. Before the first run the installed version is
	// checked and a trivial command is run under the read-only sandbox, once
	// per Backend; a machine where the sandbox cannot start is refused.
	Local Profile = iota
	// Container is for a host that already runs the process inside its own
	// boundary, such as the hosted review agent container. Codex runs with its
	// sandbox disabled: Docker's default seccomp profile blocks the user
	// namespaces the sandbox needs. The CLI may execute commands, so the child
	// receives only the run's endpoint token and run files: HOME and CODEX_HOME
	// under <run>/home, a fixed PATH and CODEX_API_KEY from the Endpoint, which
	// it requires. It never inherits the caller's environment and does not
	// check the installed version. Never use it outside such a container.
	Container
)

// standardSpeed is the pricing speed of every Codex call baum makes. Codex's
// JSON events never report a service tier, but Run fixes it: it ignores the
// user's config.toml and sets no service_tier, so Codex sends none and the API
// bills the standard tier. A change that lets a run choose a tier (Codex's
// "priority", shown as Fast) must record that tier here instead.
const standardSpeed = "standard"

// Backend runs the Codex CLI over a request's directory.
//
// The child runs in its own process group, so a terminal's Ctrl-C (SIGINT) does
// not reach it. A local caller must cancel the context it passes to Run on
// SIGINT and SIGTERM, for example with signal.NotifyContext, or the CLI keeps
// running after baum exits. Cancelling kills the group on Unix, and on Windows
// only the child itself.
type Backend struct {
	// Binary is the codex executable. The Local profile resolves it with
	// exec.LookPath and defaults to "codex".
	Binary, Model string
	Profile       Profile
	// Effort is the model_reasoning_effort; empty
	// leaves it to Codex.
	Effort string
	// Env are the user's agent variables (agent.WithUserEnv), for the Local
	// profile only.
	Env []string

	mu           sync.Mutex
	checked      bool
	checkFailure error
	version      string
}

func (*Backend) Name() string { return agent.BackendCodex }

// ConcurrentCalls reports whether calls may run over one run directory at
// once (agent.ConcurrentCaller): the Local profile writes each call's output
// schema to a temporary directory of its own. The Container profile writes it
// to CODEX_HOME under the run directory, shared by every call, so it runs
// them one at a time.
func (c *Backend) ConcurrentCalls() bool { return c.Profile == Local }

// runKind is what a run is for, which decides its arguments and what its
// result must hold.
type runKind int

const (
	// runReview submits a structured document.
	runReview runKind = iota
	// runFix edits a checkout and ends with a summary.
	runFix
	// runConverse is a turn of a person's own session (Converse).
	runConverse
)

func (c *Backend) Run(ctx context.Context, request agent.Request) (agent.Result, error) {
	return c.run(ctx, request, runReview, "", nil)
}

// Fix runs Codex with write access to the request's directory, for a fix a
// person asked for (agent.Fixer). Codex's workspace-write sandbox lets the
// commands it runs write to that directory and to Request.Scratch, which
// becomes their TMPDIR, and nowhere else (not /tmp or the caller's TMPDIR),
// with the network off. As in a review, they can read any file the user can.
// Its final message is the result's Document. Only the Local profile fixes.
func (c *Backend) Fix(ctx context.Context, request agent.Request) (agent.Result, error) {
	if c.Profile != Local {
		return agent.Result{}, errors.New("codex: only the Local profile runs fixes")
	}
	return c.run(ctx, request, runFix, "", nil)
}

// Converse runs one turn of the person's own Codex session
// (agent.Converser): Codex loads their config.toml, rules, hooks and MCP
// servers, and request.MCP joins those (converseCommand). Full access runs
// without approvals or sandbox; workspace access in the workspace-write
// sandbox, with approvals never asked for since nobody could answer. The
// session is kept, and request.ResumeSession continues it with exec resume.
// Only the Local profile converses.
func (c *Backend) Converse(ctx context.Context, request agent.Request, access agent.Access) (agent.Result, error) {
	if c.Profile != Local {
		return agent.Result{}, errors.New("codex: only the Local profile runs a person's own session")
	}
	if err := access.Validate(); err != nil {
		return agent.Result{}, fmt.Errorf("codex: %w", err)
	}
	if request.ForkSession {
		return agent.Result{}, errors.New("codex: a conversation continues its session and never forks it")
	}
	request.PersistSession = true
	request.MaxCalls, request.MaxTokens = 0, 0
	return c.run(ctx, request, runConverse, access, nil)
}

// reportUsage sends the run's usage so far (agent.Request.Usage). Codex
// reports usage per turn with no model of its own, so one attempt at the
// configured model covers the turns so far; it is Unreported until the run
// ends.
func reportUsage(request agent.Request, result agent.Result) {
	if request.Usage == nil {
		return
	}
	usage := result.Usage
	usage.Unreported = true
	request.Usage([]agent.UsageAttempt{{ModelSource: result.ModelSource, Model: result.Model, Usage: usage, Calls: result.Calls, ChargedTokens: result.Tokens}})
}

func (c *Backend) run(ctx context.Context, request agent.Request, kind runKind, access agent.Access, extra []string) (result agent.Result, err error) {
	fix := kind == runFix
	result.Model = c.Model
	result.ModelSource = "configured"
	pendingTurns := 0
	reportedTurns := 0
	defer func() {
		if pendingTurns > 0 || reportedTurns == 0 {
			result.Usage.Unreported = true
		}
	}()
	if c.Profile != Local && c.Profile != Container {
		return result, fmt.Errorf("codex: unknown profile %d", int(c.Profile))
	}
	if err := request.ValidateSession(); err != nil {
		return result, fmt.Errorf("codex: %w", err)
	}
	if request.WantsSession() && (c.Profile != Local || fix) {
		return result, errors.New("codex: only a Local profile review or task can keep, resume or fork a session")
	}
	if request.MCP != nil {
		if c.Profile != Local {
			return result, errors.New("codex: only the Local profile can use an MCP server")
		}
		if err := request.MCP.Validate(); err != nil {
			return result, fmt.Errorf("codex: %w", err)
		}
	}
	if request.MaxCalls < 0 {
		return result, errors.New(agent.GenerationBudgetExceededReason("model calls"))
	}
	if request.MaxTokens < 0 {
		return result, errors.New(agent.GenerationBudgetExceededReason("tokens"))
	}
	endpoint := request.Endpoint
	if c.Profile == Container && endpoint == nil {
		return result, errors.New("codex: the container profile needs a provider endpoint")
	}
	binary := c.Binary
	switch {
	case kind == runConverse:
		if binary, err = c.converseBinary(ctx, access); err != nil {
			return result, err
		}
	case c.Profile == Local:
		if binary, err = c.localBinary(ctx); err != nil {
			return result, err
		}
	}
	var args, env []string
	if kind == runConverse {
		args, env, err = c.converseCommand(request, access)
	} else {
		var cleanup func()
		args, env, cleanup, err = c.command(request, endpoint, fix, extra)
		defer cleanup()
	}
	if err != nil {
		return result, err
	}
	// Internal stops cancel runCtx rather than calling Cancel directly, so
	// WaitDelay bounds them as it does an outer cancellation.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	// The session id is a positional that precedes the stdin prompt marker.
	if request.ResumeSession != "" {
		args = append(args, request.ResumeSession)
	}
	cmd := exec.CommandContext(runCtx, binary, append(args, "-")...)
	cmd.Dir = request.Directory
	cmd.Env = env
	cmd.Stdin = strings.NewReader(request.Prompt)
	proc.Configure(cmd)
	// Sandboxed commands can start their own session and outlive the group
	// kill while holding stdout open; bound the wait for them after cancellation.
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
	// A person's own session may run at the service tier their config.toml
	// chooses, which Codex's events never name, so its speed is unknown.
	speed := func() *string {
		if kind == runConverse {
			return nil
		}
		return new(standardSpeed)
	}
	var document []byte
	completed := false
	// lastError is the latest "error" event's message, classified only when
	// the run fails without a message of its own.
	var lastError string
	var turnStarted time.Time
	toolStarts := map[string]time.Time{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 65536), 8<<20)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			// ThreadID is the thread.started event's session id.
			ThreadID string `json:"thread_id"`
			Item     struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Text     string `json:"text"`
				Command  string `json:"command"`
				ExitCode *int   `json:"exit_code"`
				Status   string `json:"status"`
				Output   string `json:"aggregated_output"`
				Message  string `json:"message"`
				Query    string `json:"query"`
				Server   string `json:"server"`
				Tool     string `json:"tool"`
				Changes  []struct {
					Path string `json:"path"`
					Kind string `json:"kind"`
				} `json:"changes"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage *struct {
				Input       int `json:"input_tokens"`
				CachedInput int `json:"cached_input_tokens"`
				CacheWrite  int `json:"cache_write_input_tokens"`
				Output      int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			break
		}
		request.Activity.Progress()
		if strings.HasPrefix(event.Type, "item.") {
			var paths []string
			for _, change := range event.Item.Changes {
				paths = append(paths, change.Kind+" "+agent.TitlePath(request.Directory, change.Path))
			}
			emitItem(request, event.Type, itemFields{ID: event.Item.ID, Type: event.Item.Type, Status: event.Item.Status, Text: event.Item.Text,
				Command: event.Item.Command, Output: event.Item.Output, Message: event.Item.Message, Query: event.Item.Query,
				Server: event.Item.Server, Tool: event.Item.Tool, Error: event.Item.Error.Message, ExitCode: event.Item.ExitCode, Paths: paths})
		}
		switch event.Type {
		case "thread.started":
			if event.ThreadID != "" {
				if event.ThreadID != result.SessionID && request.SessionStarted != nil {
					request.SessionStarted(event.ThreadID)
				}
				result.SessionID = event.ThreadID
			}
		case "error":
			lastError = event.Message
			request.Emit(agent.TranscriptEntry{Kind: "error", Text: event.Message})
		case "turn.started":
			// Codex exposes turn totals, not the model cycles inside each turn.
			// Count every started turn; never overwrite an earlier turn.
			result.Calls++
			pendingTurns++
			turnStarted = time.Now()
			logInfo(request, "codex turn started", "turn", result.Calls)
		case "item.started":
			if toolItems[event.Item.Type] {
				toolStarts[event.Item.ID] = time.Now()
				request.Activity.ToolStarted(event.Item.ID, event.Item.Type)
				// The command is the model's own text, bounded; its output is
				// repository content and is never logged.
				logInfo(request, "codex tool started", "item", event.Item.ID, "tool", event.Item.Type, "command", bounded(event.Item.Command, maxLoggedCommandBytes))
			}
		case "item.completed":
			if toolItems[event.Item.Type] {
				request.Activity.ToolFinished(event.Item.ID)
				attributes := []any{"item", event.Item.ID, "tool", event.Item.Type, "status", event.Item.Status}
				if started, ok := toolStarts[event.Item.ID]; ok {
					attributes = append(attributes, "duration", time.Since(started).Round(time.Millisecond))
					delete(toolStarts, event.Item.ID)
				}
				if event.Item.ExitCode != nil {
					attributes = append(attributes, "exit_code", *event.Item.ExitCode)
				}
				logInfo(request, "codex tool finished", attributes...)
			}
			if event.Item.Type == "agent_message" {
				document = []byte(strings.TrimSpace(event.Item.Text))
				if len(document) > 8<<20 {
					err = errors.New("agent document exceeds output size limit")
				}
			}
		case "turn.completed":
			completed = true
			// Unlike Anthropic's, Codex input_tokens includes cached reads and
			// cache writes, so uncached input already charges cache creation.
			if pendingTurns > 0 {
				pendingTurns--
			}
			logInfo(request, "codex turn completed", "turn", result.Calls, "duration", since(turnStarted), "usage_reported", event.Usage != nil)
			if event.Usage == nil {
				result.Usage.Unreported = true
				break
			}
			reportedTurns++
			result.Tokens += max(event.Usage.Input-event.Usage.CachedInput, 0) + event.Usage.Output
			// agent.Usage keeps Anthropic's disjoint buckets, so the cached and
			// cache-write tokens come out of Codex's input total.
			result.Usage.Add(agent.Usage{
				Input:       int64(max(event.Usage.Input-event.Usage.CachedInput-event.Usage.CacheWrite, 0)),
				Output:      int64(event.Usage.Output),
				CacheRead:   int64(event.Usage.CachedInput),
				CacheCreate: int64(event.Usage.CacheWrite),
				Speed:       speed(),
			})
			result.Events = append(result.Events, agent.Event{Kind: "model_usage", Input: event.Usage.Input, Output: event.Usage.Output,
				CacheRead: event.Usage.CachedInput, CacheCreate: event.Usage.CacheWrite})
			reportUsage(request, result)
		case "turn.failed":
			if event.Usage != nil {
				if pendingTurns > 0 {
					pendingTurns--
				}
				reportedTurns++
				result.Tokens += max(event.Usage.Input-event.Usage.CachedInput, 0) + event.Usage.Output
				result.Usage.Add(agent.Usage{Input: int64(max(event.Usage.Input-event.Usage.CachedInput-event.Usage.CacheWrite, 0)), Output: int64(event.Usage.Output), CacheRead: int64(event.Usage.CachedInput), CacheCreate: int64(event.Usage.CacheWrite), Speed: speed()})
				reportUsage(request, result)
			} else {
				result.Usage.Unreported = true
			}
			// Codex also emits "error" events for connection retries it
			// recovers from; failures it cannot recover from end the turn
			// or exit nonzero. The message may quote the run, so only its
			// classification is kept.
			message := event.Error.Message
			if message == "" {
				message = lastError
			}
			status, refused := failureStatus(message)
			result.Events = append(result.Events, agent.Event{Kind: "error", Status: status, FromProvider: refused})
			logInfo(request, "codex turn failed", "turn", result.Calls, "duration", since(turnStarted), "status", status)
			err = agent.ProviderFailure("codex", status, refused)
		}
		if err == nil && agent.Exceeds(result.Calls, request.MaxCalls) {
			err = errors.New(agent.GenerationBudgetExceededReason("model calls"))
		}
		if err != nil || agent.Exceeds(result.Tokens, request.MaxTokens) {
			if err == nil {
				err = errors.New(agent.GenerationBudgetExceededReason("tokens"))
			}
			break
		}
	}
	if scanErr := scanner.Err(); err == nil {
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
		// A resume or fork of a thread Codex cannot find exits before any
		// turn, naming it on some line of stderr (warnings may come first).
		if line, missing := agent.MissingSession(stderr.String()); missing && c.Profile == Local && request.ResumeSession != "" && result.Calls == 0 {
			return result, agent.SessionNotFound("codex", line)
		}
		if lastError != "" {
			status, structured := failureStatus(lastError)
			result.Events = append(result.Events, agent.Event{Kind: "error", Status: status, FromProvider: structured})
			// A refusal read from the provider ends the run whether or not
			// Codex reported a failed turn before exiting.
			var rejected *agent.RejectedError
			if err := agent.ProviderFailure("codex", status, structured); errors.As(err, &rejected) {
				return result, err
			}
		}
		if c.Profile == Local {
			if line, found := unknownOption(stderr.String()); found {
				if request.WantsSession() {
					return result, fmt.Errorf("this Codex does not support exec resume/fork or the options baum passes with them (%s); upgrade Codex", line)
				}
				return result, fmt.Errorf("codex does not accept the options baum needs (%s); upgrade Codex to %s or newer", line, MinimumVersion)
			}
			// Local stderr is the user's own and bounded; a hosted run's may
			// quote the repository, so Container never reports it.
			if line := firstLine(stderr.String()); line != "" {
				return result, fmt.Errorf("codex failed: %w (%s)", waitErr, line)
			}
		}
		return result, fmt.Errorf("codex failed: %w", waitErr)
	}
	if kind == runConverse {
		if !completed {
			return result, errors.New("codex ended without finishing its answer")
		}
		result.Document = []byte(bounded(strings.TrimSpace(string(document)), agent.MaxConverseBytes))
		return result, nil
	}
	if fix {
		if !completed {
			return result, errors.New("codex did not finish the fix")
		}
		result.Document = []byte(bounded(strings.TrimSpace(string(document)), agent.MaxFixSummaryBytes))
		return result, nil
	}
	if !completed || len(document) == 0 || bytes.Equal(document, []byte("null")) {
		return result, errors.New("codex returned no review document")
	}
	result.Document = document
	result.Model = c.Model
	result.Events = append(result.Events, agent.Event{Kind: "document", Bytes: len(document)})
	return result, nil
}

// unexpectedStatus is how Codex names the HTTP status of a provider response
// it gave up on, other than a 400 (whose message is the response body) and
// an exhausted 429 ("last status: 429").
var unexpectedStatus = regexp.MustCompile(`unexpected status (\d{3})\b`)

// invalidSchema is the error code of a response format the provider refused.
var invalidSchema = regexp.MustCompile(`"code":\s*"invalid_json_schema"`)

// bodyMarkers are structured parts of a provider response Codex quotes
// without naming its status, each with the class it gives.
var bodyMarkers = []struct {
	pattern *regexp.Regexp
	status  string
}{
	{invalidSchema, "invalid_schema"},
	{regexp.MustCompile(`"type":\s*"invalid_request_error"`), "invalid_request"},
	{regexp.MustCompile(`last status: 429\b`), "rate_limit"},
}

// failureStatus classifies a failed turn's message and reports whether the
// class was read from the provider rather than guessed from free text: only
// such a class may end the run as a refusal. A named HTTP status decides alone (a
// 400 whose body names an invalid schema is invalid_schema), so a 408 or 5xx
// is never a refusal whatever its body quotes; without one, only a structured
// marker of the body may refuse. Free text is classified for the diagnostics
// only: a request ID or a quoted run can hold "400" or "authentication".
func failureStatus(message string) (string, bool) {
	if match := unexpectedStatus.FindStringSubmatch(message); match != nil {
		code, _ := strconv.Atoi(match[1])
		if code == 400 && invalidSchema.MatchString(message) {
			return "invalid_schema", true
		}
		if status := agent.HTTPErrorStatus(code); status != "" {
			return status, true
		}
		return "error", false
	}
	for _, marker := range bodyMarkers {
		if marker.pattern.MatchString(message) {
			return marker.status, true
		}
	}
	return agent.ErrorStatus(message), false
}

// since is how long ago a turn started, or zero when none did.
func since(started time.Time) time.Duration {
	if started.IsZero() {
		return 0
	}
	return time.Since(started).Round(time.Millisecond)
}

// toolItems are the item types of Codex's JSON events that run a tool. Their
// item.started and item.completed bracket the tool's execution.
var toolItems = map[string]bool{"command_execution": true, "file_change": true, "mcp_tool_call": true, "web_search": true}

// maxLoggedCommandBytes bounds a command in the logs.
const maxLoggedCommandBytes = 512

func logInfo(request agent.Request, message string, attributes ...any) {
	if request.Logger != nil {
		request.Logger.Info(message, attributes...)
	}
}

// bounded cuts text to at most limit bytes on a rune boundary.
func bounded(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}

// Keep diagnostic output bounded; it is never exposed in a hosted failure.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 65536 {
		_, _ = b.Buffer.Write(p[:min(len(p), 65536-b.Len())])
	}
	return n, nil
}

// mcpConfig configures baum's own MCP server, the only one a run has:
// --ignore-user-config already keeps the user's servers out. Codex reaches it
// over HTTP and reads the bearer token from the variable that
// bearer_token_env_var names in its own environment, so the token is in no
// argument.
func mcpConfig(server *agent.MCPServer) ([]string, error) {
	url, err := json.Marshal(server.URL)
	if err != nil {
		return nil, err
	}
	name, err := json.Marshal(agent.MCPTokenEnv)
	if err != nil {
		return nil, err
	}
	return []string{"-c", "mcp_servers.baum.url=" + string(url), "-c", "mcp_servers.baum.bearer_token_env_var=" + string(name)}, nil
}
