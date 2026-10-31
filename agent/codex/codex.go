package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
	// Effort is the model_reasoning_effort (low, medium or high); empty
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

func (c *Backend) Run(ctx context.Context, request agent.Request) (agent.Result, error) {
	return c.run(ctx, request, false, nil)
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
	return c.run(ctx, request, true, nil)
}

func (c *Backend) run(ctx context.Context, request agent.Request, fix bool, extra []string) (result agent.Result, err error) {
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
	if request.MaxCalls < 1 {
		return result, errors.New(agent.GenerationBudgetExceededReason("model calls"))
	}
	if request.MaxTokens < 1 {
		return result, errors.New(agent.GenerationBudgetExceededReason("tokens"))
	}
	endpoint := request.Endpoint
	if c.Profile == Container && endpoint == nil {
		return result, errors.New("codex: the container profile needs a provider endpoint")
	}
	binary := c.Binary
	if c.Profile == Local {
		if binary, err = c.localBinary(ctx); err != nil {
			return result, err
		}
	}
	var codexHome string
	var env []string
	var schemaDirectory string
	sandbox := "read-only"
	if c.Profile == Container {
		sandbox = "danger-full-access"
		// Codex refuses to start when CODEX_HOME does not exist.
		codexHome = filepath.Join(request.Directory, "home", ".codex")
		if err := os.MkdirAll(codexHome, 0700); err != nil {
			return result, err
		}
		schemaDirectory = codexHome
		env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Join(request.Directory, "home"),
			"CODEX_HOME=" + codexHome, "CODEX_API_KEY=" + endpoint.Key}
	} else {
		env = agent.WithUserEnv(agent.LocalEnvironment(os.Environ(), "CODEX_HOME"), c.Env)
		if endpoint != nil {
			env = append(env, "CODEX_API_KEY="+endpoint.Key)
		}
	}
	var fixConfig []string
	if fix {
		sandbox = "workspace-write"
		fixConfig = []string{"-c", "sandbox_workspace_write.network_access=false",
			"-c", "sandbox_workspace_write.exclude_slash_tmp=true", "-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true"}
		if request.Scratch != "" {
			encoded, err := json.Marshal([]string{request.Scratch})
			if err != nil {
				return result, err
			}
			fixConfig = append(fixConfig, "-c", "sandbox_workspace_write.writable_roots="+string(encoded))
			env = slices.DeleteFunc(env, func(entry string) bool { return strings.HasPrefix(entry, "TMPDIR=") })
			env = append(env, "TMPDIR="+request.Scratch)
		}
	}
	// Without session options the argv is exactly the ephemeral one. A kept
	// session omits --ephemeral. `exec resume` and `exec fork` do not accept
	// --sandbox, so the sandbox goes in as config there. Both subcommands
	// postdate the version floor's release notes as far as known; a Codex too
	// old for them fails with its own usage error.
	args := []string{"exec"}
	switch {
	case request.ResumeSession != "" && request.ForkSession:
		args = append(args, "fork")
	case request.ResumeSession != "":
		args = append(args, "resume")
	}
	args = append(args, "--json")
	if !request.KeepsSession() {
		args = append(args, "--ephemeral")
	}
	args = append(args, "--ignore-user-config", "--ignore-rules",
		"--disable", "hooks", "--disable", "apps", "--disable", "multi_agent")
	if request.ResumeSession != "" {
		args = append(args, "-c", "sandbox_mode="+strconv.Quote(sandbox))
	} else {
		args = append(args, "--sandbox", sandbox)
	}
	args = append(args, "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"--skip-git-repo-check")
	args = append(args, fixConfig...)
	args = append(args, extra...)
	if c.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(c.Effort))
	}
	// An empty Model leaves the flag out so Codex uses its configured default.
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if endpoint != nil && endpoint.BaseURL != "" {
		encoded, err := json.Marshal(endpoint.BaseURL)
		if err != nil {
			return result, err
		}
		args = append(args, "-c", "openai_base_url="+string(encoded))
	}
	if len(request.Schema) > 0 && !fix {
		schema, err := StrictSchema(request.Schema)
		if err != nil {
			return result, fmt.Errorf("deriving Codex output schema: %w", err)
		}
		if schemaDirectory == "" {
			// A private directory of its own: the user's CODEX_HOME holds their
			// login and configuration and is never written to.
			if schemaDirectory, err = os.MkdirTemp("", "baum-codex-schema-"); err != nil {
				return result, err
			}
			defer os.RemoveAll(schemaDirectory)
		}
		schemaPath := filepath.Join(schemaDirectory, "output-schema.json")
		if err := os.WriteFile(schemaPath, schema, 0600); err != nil {
			return result, err
		}
		args = append(args, "--output-schema", schemaPath)
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
			})
			result.Events = append(result.Events, agent.Event{Kind: "model_usage", Input: event.Usage.Input, Output: event.Usage.Output,
				CacheRead: event.Usage.CachedInput, CacheCreate: event.Usage.CacheWrite})
		case "turn.failed":
			if event.Usage != nil {
				if pendingTurns > 0 {
					pendingTurns--
				}
				reportedTurns++
				result.Tokens += max(event.Usage.Input-event.Usage.CachedInput, 0) + event.Usage.Output
				result.Usage.Add(agent.Usage{Input: int64(max(event.Usage.Input-event.Usage.CachedInput-event.Usage.CacheWrite, 0)), Output: int64(event.Usage.Output), CacheRead: int64(event.Usage.CachedInput), CacheCreate: int64(event.Usage.CacheWrite)})
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
		if err == nil && result.Calls > request.MaxCalls {
			err = errors.New(agent.GenerationBudgetExceededReason("model calls"))
		}
		if err != nil || result.Tokens > request.MaxTokens {
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
				return result, fmt.Errorf("codex does not accept the options baum needs (%s); upgrade Codex to %s or newer", line, minimumVersion)
			}
			// Local stderr is the user's own and bounded; a hosted run's may
			// quote the repository, so Container never reports it.
			if line := firstLine(stderr.String()); line != "" {
				return result, fmt.Errorf("codex failed: %w (%s)", waitErr, line)
			}
		}
		return result, fmt.Errorf("codex failed: %w", waitErr)
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
