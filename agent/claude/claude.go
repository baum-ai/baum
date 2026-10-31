// Package claude runs the Claude Code CLI as an agent.Backend: a read-only
// model call loop over a directory whose final answer is a JSON document.
package claude

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/proc"
)

// maxOutputTokens bounds one model response. A large review submitted through
// the StructuredOutput tool is a single tool input, so a cap sized for prose
// truncates it into invalid JSON and forces another full call.
const maxOutputTokens = 32000

// Profile selects the environment and credentials the Claude Code process gets.
// Both profiles run it with the same read-only flags.
type Profile int

const (
	// User is for local runs and the zero value. The process gets the user's
	// own HOME and login, an allowlisted copy of the environment
	// (agent.LocalEnvironment, plus CLAUDE_CONFIG_DIR) and the binary found on
	// PATH. A provider key and base URL are set only when the request or
	// DefaultEndpoint names an Endpoint. The installed version is checked
	// once before the first run.
	User Profile = iota
	// Isolated is for hosted and evaluation runs: HOME and CLAUDE_CONFIG_DIR
	// under <run>/home, a fixed PATH and the provider key and base URL from
	// the Endpoint, which it requires. It never inherits the caller's
	// environment, and it does not check the installed version.
	Isolated
)

// Backend runs the Claude Code CLI over a request's directory.
//
// The child runs in its own process group, so a terminal's Ctrl-C (SIGINT) does
// not reach it. A local caller must cancel the context it passes to Run on
// SIGINT and SIGTERM, for example with signal.NotifyContext, or the CLI keeps
// running after baum exits. Cancelling kills the group on Unix, and on Windows
// only the child itself.
type Backend struct {
	// Binary is the claude executable. The User profile resolves it with
	// exec.LookPath and defaults to "claude".
	Binary, Model string
	Profile       Profile
	// Effort is the --effort level (low, medium or high); empty leaves it to
	// Claude Code. The User profile leaves thinking to Claude Code, while the
	// Isolated profile always runs without thinking.
	Effort string
	// DefaultEndpoint is a provider key and base URL used when the request has
	// no Endpoint of its own. The hosted review agent never sets it: its runs
	// bring a model proxy endpoint. Local prompt evaluation sets it because it
	// runs on a developer's host without a proxy.
	DefaultEndpoint *agent.Endpoint
	// Env are the user's agent variables (agent.WithUserEnv), for the User
	// profile only.
	Env []string

	mu         sync.Mutex
	versionOK  bool
	versionErr error
	version    string
}

func (*Backend) Name() string { return agent.BackendClaude }

func (c *Backend) Run(ctx context.Context, request agent.Request) (agent.Result, error) {
	return c.run(ctx, request, c.args(request), nil, false)
}

// Fix runs Claude Code with write access to the request's directory, for a
// fix a person asked for (agent.Fixer). It runs in restricted mode with the
// file tools only (Read, Glob, Grep, Edit and Write), confined to the
// directory: it cannot run a command, so it cannot run tests, git or anything
// else, and the caller commits what it changed. Its final message is the
// result's Document. Only the User profile fixes.
func (c *Backend) Fix(ctx context.Context, request agent.Request) (agent.Result, error) {
	if c.Profile != User {
		return agent.Result{}, errors.New("claude: only the User profile runs fixes")
	}
	return c.run(ctx, request, c.fixArgs(request), nil, true)
}

func (c *Backend) run(ctx context.Context, request agent.Request, args, env []string, fix bool) (result agent.Result, err error) {
	if c.Profile != User && c.Profile != Isolated {
		return result, fmt.Errorf("claude: unknown profile %d", int(c.Profile))
	}
	if err := request.ValidateSession(); err != nil {
		return result, fmt.Errorf("claude: %w", err)
	}
	if request.WantsSession() && (c.Profile != User || fix) {
		return result, errors.New("claude: only a User profile review or task can keep, resume or fork a session")
	}
	if request.MaxCalls < 0 {
		return result, errors.New(agent.GenerationBudgetExceededReason("model calls"))
	}
	if request.MaxTokens < 0 {
		return result, errors.New(agent.GenerationBudgetExceededReason("tokens"))
	}
	endpoint := cmp.Or(request.Endpoint, c.DefaultEndpoint)
	if c.Profile == Isolated && endpoint == nil {
		return result, errors.New("claude: the isolated profile needs a provider endpoint")
	}
	binary := c.Binary
	if c.Profile == User {
		if binary, err = c.userBinary(ctx); err != nil {
			return result, err
		}
	}
	started := time.Now()
	record := func(event agent.Event) {
		event.ElapsedMS = time.Since(started).Milliseconds()
		event.ID = event.ID[:min(len(event.ID), 128)]
		event.Tool = event.Tool[:min(len(event.Tool), 64)]
		if len(result.Events) == 256 {
			if event.Kind != "finished" {
				return
			}
			result.Events[255] = event
		} else {
			result.Events = append(result.Events, event)
		}
		if request.Logger != nil {
			request.Logger.Info("review agent event", "kind", event.Kind, "elapsed_ms", event.ElapsedMS, "id", event.ID, "tool", event.Tool, "path", event.Path, "offset", event.Offset, "limit", event.Limit, "bytes", event.Bytes, "status", event.Status, "input_tokens", event.Input, "output_tokens", event.Output, "cache_read_tokens", event.CacheRead, "cache_creation_tokens", event.CacheCreate)
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
	// The document is submitted through the StructuredOutput tool that
	// --json-schema registers. Text output cannot be validated, so the
	// instructions never ask for JSON as text: a text answer only costs a
	// truncated response and a second call.
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = request.Directory
	// A later entry of the same variable wins (exec.Cmd.Env).
	cmd.Env = append(c.environment(request.Directory, endpoint), env...)
	cmd.Stdin = strings.NewReader(request.Prompt)
	// Kill the process group so cancellation also stops harness subprocesses.
	proc.Configure(cmd)
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
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 65536), 8<<20)
	type usage = claudeUsage
	// Usage is kept per model call (message ID) from every report of it:
	// message_start, the assistant snapshots and message_delta. A snapshot
	// carries the output count at the time it was taken, and message_delta's
	// usage is cumulative, so each bucket keeps the largest value seen and the
	// last message_delta of a call leaves its final count; a bucket a delta
	// omits or reports as 0 keeps message_start's value. A run cut off before
	// its result keeps what its calls reported so far. The result's usage is
	// Claude Code's own total for the run: each bucket keeps the larger of it
	// and the sum over calls, so a call the stream did not report still
	// counts, and a call the total leaves out (one whose stream died) is not
	// lost.
	larger := func(a, b usage) usage {
		merged := usage{Input: max(a.Input, b.Input), Output: max(a.Output, b.Output), CacheRead: max(a.CacheRead, b.CacheRead), CacheCreate: max(a.CacheCreate, b.CacheCreate)}
		if ha, hb := a.Cache1h(), b.Cache1h(); ha != nil || hb != nil {
			merged.CacheCreation = cacheSplit{Hour: pointerTo(max(intOr(ha, 0), intOr(hb, 0)))}
		}
		merged.Speed = cmp.Or(b.Speed, a.Speed)
		return merged
	}
	type modelCall struct {
		usage           usage
		model           string
		streamed, final bool
		// counted: the call had a snapshot or a message_delta. A call whose
		// stream dies after message_start is retried under a new ID, so
		// counting it would charge one turn twice against MaxCalls; its
		// usage still counts.
		counted bool
		// logged: its model_usage diagnostic is recorded, once per call, at its
		// message_stop or, for a call without one, at the end of the run.
		logged bool
	}
	calls := map[string]*modelCall{}
	var callOrder []string
	var reportedTotal usage
	streaming := ""
	total := func() {
		var sum usage
		// splitKnown: every call that wrote to the cache reported its 1-hour part.
		splitKnown := true
		var speed lenientString
		speedKnown := true
		// The per-call prompt range is known when every call reported a prompt.
		rangeKnown := len(calls) > 0
		var lowest, highest int
		for _, call := range calls {
			prompt := call.usage.Input + call.usage.CacheRead + call.usage.CacheCreate
			if prompt == 0 {
				rangeKnown = false
			} else {
				lowest = cmp.Or(min(lowest, prompt), prompt)
				highest = max(highest, prompt)
			}
			sum.Input += call.usage.Input
			sum.Output += call.usage.Output
			sum.CacheRead += call.usage.CacheRead
			sum.CacheCreate += call.usage.CacheCreate
			h := call.usage.Cache1h()
			if h != nil {
				sum.CacheCreation = cacheSplit{Hour: pointerTo(intOr(sum.Cache1h(), 0) + *h)}
			} else if call.usage.CacheCreate > 0 {
				splitKnown = false
			}
			// A call with no usage decides neither the speed nor the split.
			if call.usage.Input+call.usage.Output+call.usage.CacheRead+call.usage.CacheCreate == 0 {
				continue
			}
			if call.usage.Speed == "" || (speed != "" && call.usage.Speed != speed) {
				speedKnown = false
			}
			speed = call.usage.Speed
		}
		u := larger(sum, reportedTotal)
		result.Usage = agent.Usage{Input: int64(u.Input), Output: int64(u.Output), CacheRead: int64(u.CacheRead), CacheCreate: int64(u.CacheCreate)}
		// A run total above what its calls sum to includes a call the stream
		// did not report, whose prompt is unknown.
		if reportedTotal.Input > sum.Input || reportedTotal.CacheRead > sum.CacheRead || reportedTotal.CacheCreate > sum.CacheCreate {
			rangeKnown = false
		}
		if rangeKnown {
			result.Usage.MinPrompt, result.Usage.MaxPrompt = pointerTo(int64(lowest)), pointerTo(int64(highest))
		}
		// The 1-hour part is known only if its source reported a split: the
		// run's own total when it is the larger figure, else every call that
		// wrote to the cache. It cannot exceed the cache writes it belongs to.
		hour := reportedTotal.Cache1h()
		if reportedTotal.CacheCreate <= sum.CacheCreate {
			hour = nil
			if splitKnown {
				hour = sum.Cache1h()
			}
		}
		if hour != nil && *hour >= 0 && *hour <= u.CacheCreate {
			result.Usage.CacheCreate1h = pointerTo(int64(*hour))
		}
		// A run's speed is known only when every call that used tokens reported
		// the same one.
		if speedKnown && (speed == speedStandard || speed == speedFast) {
			value := string(speed)
			result.Usage.Speed = &value
		}
		result.Tokens = agent.ChargedTokens(u.Input, u.Output, u.CacheCreate)
	}
	logCall := func(id string) {
		if call := calls[id]; call != nil && !call.logged {
			call.logged = true
			u := call.usage
			record(agent.Event{Kind: "model_usage", ID: id, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheCreate: u.CacheCreate})
		}
	}
	observe := func(id string, reported usage, counts bool) {
		call := calls[id]
		if call == nil {
			call = &modelCall{}
			calls[id] = call
			callOrder = append(callOrder, id)
		}
		if counts && !call.counted {
			call.counted = true
			result.Calls++
		}
		call.usage = larger(call.usage, reported)
		total()
	}
	completed := false
	toolsSeen := map[string]string{}
	titles := map[string]string{}
	done := make(chan struct{})
	defer close(done)
	if request.Logger != nil {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					request.Logger.Info("review agent waiting", "elapsed", time.Since(started))
				}
			}
		}()
	}
	for scanner.Scan() {
		var event struct {
			Type, Subtype string
			SessionID     string   `json:"session_id"`
			Tools         []string `json:"tools"`
			IsError       bool     `json:"is_error"`
			Errors        []string `json:"errors"`
			// NumTurns is how many turns a result's run took.
			NumTurns int `json:"num_turns"`
			// APIErrorStatus is the HTTP status of the provider response
			// a failed result ended on, when it ended on one.
			APIErrorStatus int `json:"api_error_status"`
			Message        struct {
				ID, Model string
				Content   []struct {
					Type, ID, Name string
					Text, Thinking string
					ToolUseID      string `json:"tool_use_id"`
					IsError        bool   `json:"is_error"`
					Content        json.RawMessage
					Input          struct {
						FilePath      string `json:"file_path"`
						Path          string
						Pattern       string
						Command       string
						Offset, Limit int
					}
				}
				StopReason string `json:"stop_reason"`
				Usage      usage
			} `json:"message"`
			Event struct {
				Type    string
				Message struct {
					ID, Model string
					Usage     usage
				}
				Delta struct {
					StopReason string `json:"stop_reason"`
				}
				Usage usage
			} `json:"event"`
			Usage    usage           `json:"usage"`
			Document json.RawMessage `json:"structured_output"`
			// Result is the final message's text, which a fix keeps.
			Result string `json:"result"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			break
		}
		request.Activity.Progress()
		// The init event's session_id wins; any other event's (the result's)
		// fills in only while none is known.
		if event.SessionID != "" && (event.Type == "system" && event.Subtype == "init" || result.SessionID == "") {
			result.SessionID = event.SessionID
		}

		if event.Type == "stream_event" {
			switch event.Event.Type {
			case "message_start":
				streaming = event.Event.Message.ID
				if streaming != "" {
					observe(streaming, event.Event.Message.Usage, false)
					calls[streaming].streamed = true
				}
				// A call cut off before its first snapshot still names its model.
				if event.Event.Message.Model != "" {
					result.Model = event.Event.Message.Model
					if call := calls[streaming]; call != nil {
						call.model = event.Event.Message.Model
					}
				}
			case "message_delta":
				record(agent.Event{Kind: "response", Status: event.Event.Delta.StopReason})
				if streaming != "" {
					observe(streaming, event.Event.Usage, true)
					if event.Event.Delta.StopReason != "" {
						calls[streaming].final = true
					}
				}
			case "message_stop":
				// A call may send several message_delta events, each
				// cumulative; after the last its usage is final.
				if streaming != "" {
					calls[streaming].final = true
					logCall(streaming)
				}
				streaming = ""
			}
		}

		for _, content := range event.Message.Content {
			if _, seen := toolsSeen[content.ID]; content.Type == "tool_use" && !seen {
				toolsSeen[content.ID] = content.Name
				titles[content.ID] = toolTitle(content.Name, agent.TitlePath(request.Directory, content.Input.FilePath), agent.TitlePath(request.Directory, content.Input.Path), content.Input.Pattern, content.Input.Command)
				request.Emit(agent.TranscriptEntry{Kind: "tool", ID: content.ID, Tool: content.Name, Title: titles[content.ID], Status: "running"})
				request.Activity.ToolStarted(content.ID, content.Name)
				path := agent.EventPath(request.Directory, cmp.Or(content.Input.FilePath, content.Input.Path))
				record(agent.Event{Kind: "tool_call", ID: content.ID, Tool: content.Name, Path: path, Offset: content.Input.Offset, Limit: content.Input.Limit})
			}
			if event.Type == "assistant" && content.Type == "text" && strings.TrimSpace(content.Text) != "" {
				request.Emit(agent.TranscriptEntry{Kind: "message", Text: content.Text})
			}
			if event.Type == "assistant" && content.Type == "thinking" && strings.TrimSpace(content.Thinking) != "" {
				request.Emit(agent.TranscriptEntry{Kind: "reasoning", Text: content.Thinking})
			}
			if content.Type == "tool_result" {
				request.Activity.ToolFinished(content.ToolUseID)
				finished := "completed"
				if content.IsError {
					finished = "failed"
				}
				request.Emit(agent.TranscriptEntry{Kind: "tool", ID: content.ToolUseID, Tool: toolsSeen[content.ToolUseID], Title: titles[content.ToolUseID], Text: toolResultText(content.Content), Status: finished})
				status := "success"
				tool := toolsSeen[content.ToolUseID]
				if content.IsError && tool == structuredOutputTool {
					// The harness rejected the submitted document against the
					// schema. Its message describes the document's shape, not
					// repository content, and is the only record of why a
					// call was wasted, so a bounded copy goes to the private log.
					status = "schema_rejected"
					result.StructuredOutputRejections++
					result.LastRejection = boundedText(toolResultText(content.Content), 512)
					if request.Logger != nil {
						request.Logger.Info("structured output rejected", "id", content.ToolUseID, "detail", result.LastRejection)
					}
				} else if content.IsError {
					status = agent.ErrorStatus(string(content.Content))
				}
				record(agent.Event{Kind: "tool_result", ID: content.ToolUseID, Tool: tool, Bytes: len(content.Content), Status: status})
			}
		}
		if event.Type == "assistant" {
			if event.Message.ID == "" {
				err = errors.New("agent response has no model-call ID")
				break
			}
			observe(event.Message.ID, event.Message.Usage, true)
			result.Model = event.Message.Model
			calls[event.Message.ID].model = event.Message.Model
			if event.Message.StopReason != "" {
				calls[event.Message.ID].final = true
			}
		}
		if event.Type == "result" {
			u := event.Usage
			record(agent.Event{Kind: "result", Status: event.Subtype, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheCreate: u.CacheCreate})
			reportedTotal = u
			total()
			if event.Subtype == "error_max_turns" {
				err = errors.New(agent.GenerationBudgetExceededReason("model calls"))
				break
			}
			if event.IsError || event.Subtype != "success" {
				// A resume or fork of a session Claude Code cannot find ends
				// at once with a failed result naming it; on a fork the
				// result carries a fresh session id that holds nothing.
				if line, missing := agent.MissingSession(strings.Join(event.Errors, "\n")); missing && request.ResumeSession != "" && event.NumTurns == 0 {
					record(agent.Event{Kind: "error", Status: "session_not_found"})
					result.SessionID = ""
					err = agent.SessionNotFound("claude code", line)
					break
				}
				// Only the provider's status may refuse; the errors' text is
				// classified for the diagnostics alone.
				status := agent.HTTPErrorStatus(event.APIErrorStatus)
				refused := status != ""
				switch {
				case refused:
				case event.APIErrorStatus != 0:
					status = "error"
				default:
					status = agent.ErrorStatus(strings.Join(event.Errors, " "))
				}
				record(agent.Event{Kind: "error", Status: status, FromProvider: refused})
				err = agent.ProviderFailure("claude code", status, refused)
				break
			}
			result.Document = bytes.TrimSpace(event.Document)
			if fix {
				result.Document = []byte(boundedSummary(event.Result))
			}
			if len(result.Document) > 8<<20 {
				err = errors.New("agent document exceeds output size limit")
				break
			}
			record(agent.Event{Kind: "document", Bytes: len(result.Document)})
			completed = true
		}
		if agent.Exceeds(result.Calls, request.MaxCalls) {
			err = errors.New(agent.GenerationBudgetExceededReason("model calls"))
			break
		}
		if agent.Exceeds(result.Tokens, request.MaxTokens) {
			err = errors.New(agent.GenerationBudgetExceededReason("tokens"))
			break
		}
	}
	if scanErr := scanner.Err(); err == nil {
		err = scanErr
	}
	// A call that never sent its message_stop (cut off, or reported only by
	// snapshots) is logged with its latest usage.
	for _, id := range callOrder {
		logCall(id)
	}
	if err != nil {
		_ = cmd.Cancel()
		_, _ = io.Copy(io.Discard, stdout)
	}
	// Preserve the per-call reports before the backend's aggregate loses
	// model identity. A run-total excess cannot be assigned to a model.
	var assigned agent.Usage
	for _, id := range callOrder {
		call := calls[id]
		u := call.usage
		usage := agent.Usage{Input: int64(u.Input), Output: int64(u.Output), CacheRead: int64(u.CacheRead), CacheCreate: int64(u.CacheCreate)}
		if h := u.Cache1h(); h != nil {
			usage.CacheCreate1h = pointerTo(int64(*h))
		}
		if u.Speed == speedStandard || u.Speed == speedFast {
			usage.Speed = pointerTo(string(u.Speed))
		}
		prompt := int64(u.Input + u.CacheRead + u.CacheCreate)
		if prompt > 0 {
			usage.MinPrompt, usage.MaxPrompt = pointerTo(prompt), pointerTo(prompt)
		}
		complete := call.final || (!call.streamed && completed)
		if !complete || usage.IsZero() {
			usage.Unreported = true
			result.Usage.Unreported = true
		}
		assigned.Add(usage)
		result.UsageAttempts = append(result.UsageAttempts, agent.UsageAttempt{Model: call.model, ModelSource: "provider_reported", Usage: usage, Calls: 1, ChargedTokens: agent.ChargedTokens(u.Input, u.Output, u.CacheCreate), Failed: !complete})
	}
	residual := agent.Usage{Input: max(0, result.Usage.Input-assigned.Input), Output: max(0, result.Usage.Output-assigned.Output), CacheRead: max(0, result.Usage.CacheRead-assigned.CacheRead), CacheCreate: max(0, result.Usage.CacheCreate-assigned.CacheCreate)}
	if !residual.IsZero() {
		result.UsageAttempts = append(result.UsageAttempts, agent.UsageAttempt{Usage: residual, ChargedTokens: int(residual.Input + residual.Output + residual.CacheCreate), Failed: !completed})
	}
	if !completed {
		result.Usage.Unreported = true
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		return result, err
	}
	if waitErr != nil {
		if line, missing := agent.MissingSession(stderr.String()); missing && request.ResumeSession != "" && result.Calls == 0 {
			result.SessionID = ""
			return result, agent.SessionNotFound("claude code", line)
		}
		if c.Profile == User && unknownOption(stderr.String()) {
			return result, fmt.Errorf("claude does not accept the options baum needs (%s); upgrade Claude Code to %s or newer", firstLine(stderr.String()), minimumVersion)
		}
		return result, fmt.Errorf("claude code failed: %w (%s)", waitErr, stderr.String())
	}
	if fix && !completed {
		return result, errors.New("claude code did not finish the fix")
	}
	if !fix && (!completed || len(result.Document) == 0 || bytes.Equal(result.Document, []byte("null"))) {
		return result, errors.New("claude code returned no structured document")
	}
	return result, nil
}

// boundedSummary trims a fix's final message to agent.MaxFixSummaryBytes.
func boundedSummary(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > agent.MaxFixSummaryBytes {
		text = strings.ToValidUTF8(text[:agent.MaxFixSummaryBytes], "") + "…"
	}
	return text
}

// toolTitle names a tool call for the transcript: the tool and its main
// argument, the first of path, pattern and command that it has.
func toolTitle(name string, arguments ...string) string {
	if argument := cmp.Or(arguments...); argument != "" {
		return name + " " + argument
	}
	return name
}

const structuredOutputTool = "StructuredOutput"

// toolResultText flattens a tool result, which the harness emits either as a
// string or as an array of text blocks.
func toolResultText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct{ Text string }
	if json.Unmarshal(content, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			parts = append(parts, block.Text)
		}
		return strings.Join(parts, " ")
	}
	return string(content)
}

func boundedText(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, text)
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

// Keep diagnostic output bounded; it is logged privately and never used as a job reason.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 65536 {
		_, _ = b.Buffer.Write(p[:min(len(p), 65536-b.Len())])
	}
	return n, nil
}
