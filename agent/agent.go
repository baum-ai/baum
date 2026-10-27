// Package agent defines the model backend interface and the types a run
// exchanges with it: the request, the result, the usage and the event log, plus
// the run budgets, the failure reasons, error classification and the
// environment a local CLI backend's child process receives.
package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

// Backend names, as config.Backend accepts them.
const (
	BackendClaude      = "claude"
	BackendCodex       = "codex"
	BackendAnthropic   = "anthropic"
	BackendCursorAgent = "cursor-agent"
)

// RunDirPrefix is the convention for the name of the temporary directory a
// local caller runs a backend in (os.MkdirTemp("", agent.RunDirPrefix+"*")).
// Nothing in this package creates it. Session discovery skips sessions recorded
// under such a directory as a backstop; the primary protection is that the
// backend persists no session and loads no hooks.
const RunDirPrefix = "baum-agent-run-"

// Backend runs one model call loop over a checkout and returns the document
// the model submitted.
type Backend interface {
	Name() string
	Run(context.Context, Request) (Result, error)
}

// Endpoint is a complete provider base URL and the key or token that
// authenticates to it.
type Endpoint struct {
	BaseURL string
	Key     string
}

// Budgets bounds one run. MaxTokens counts tokens the run pays for in full
// (input, output and cache creation); cache reads repeat the same context on
// every call and are reported but not charged against it.
type Budgets struct {
	WallClock           time.Duration `json:"wall_clock"`
	MaxCalls            int           `json:"max_calls"`
	MaxTokens           int           `json:"max_tokens"`
	MaxProviderAttempts int           `json:"max_provider_attempts"`
	MaxRepairAttempts   int           `json:"max_repair_attempts"`
	// StallTimeout ends an attempt that has no model request open and no
	// tool running and has made no progress for this long; ToolTimeout ends
	// one whose single tool runs longer. Zero disables each. An attempt ended
	// either way is retried once, then the run fails with ReasonStalled.
	StallTimeout time.Duration `json:"stall_timeout,omitempty"`
	ToolTimeout  time.Duration `json:"tool_timeout,omitempty"`
}

type Request struct {
	Logger              *slog.Logger
	Directory, Prompt   string
	Schema              []byte
	MaxCalls, MaxTokens int
	// Endpoint replaces the backend's default provider address and key. Nil
	// leaves the backend's own.
	Endpoint *Endpoint
	// Activity receives the backend's progress, tools and turns for the
	// stall watchdog; nil when the run has none.
	Activity *Activity
	// Task marks a generic task (Generator.RunTask) rather than a review, so
	// the backend frames it without pull request review wording.
	Task bool
	// Scratch, for a Fixer, is a private directory the agent's commands may
	// write to besides Directory: their TMPDIR. Empty leaves the backend's
	// own temporary directory.
	Scratch string
	// Transcript, when set, receives each step of the run as the person
	// watching sees it (see TranscriptEntry); nil when none is kept.
	Transcript func(TranscriptEntry)
}

// TranscriptEntry is one step of a run as the person watching it sees it:
// local only, never sent anywhere, and never part of an Event. Entries are
// append-only: a tool that starts is a "tool" entry with status "running";
// when it ends, another with the same ID, status "completed" or "failed" and
// its output in Text. A consumer merges them by ID.
type TranscriptEntry struct {
	Time time.Time `json:"time"`
	// Kind is "message", "reasoning", "tool" or "error".
	Kind string `json:"kind"`
	// ID, Tool, Title and Status are a tool entry's: the harness's id, the
	// tool ("command", "file_change", "Read", ...), its command line or name
	// and main argument, and "running", "completed" or "failed".
	ID     string `json:"id,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
	// Text is a message's or reasoning's text, or a finished tool's output.
	Text string `json:"text,omitempty"`
}

// Emit sends an entry to Transcript, stamped with the time unless it has one.
// It does nothing when there is no Transcript.
func (r Request) Emit(entry TranscriptEntry) {
	if r.Transcript == nil {
		return
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	r.Transcript(entry)
}

type Result struct {
	Events                     []Event
	Document                   []byte
	Model                      string
	Calls, Tokens              int
	UsageAttempts              []UsageAttempt
	ModelSource                string
	Usage                      Usage
	StructuredOutputRejections int
}

// Event stores bounded metadata, never assistant text, tool output or search patterns.
type Event struct {
	Kind      string `json:"kind"`
	ElapsedMS int64  `json:"elapsed_ms"`
	ID        string `json:"id,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Path      string `json:"path,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	Status    string `json:"status,omitempty"`
	// FromProvider marks an error event whose Status was read from the
	// provider's HTTP status or a structured field of its response, not
	// guessed from free text.
	FromProvider bool `json:"from_provider,omitempty"`
	Input        int  `json:"input_tokens,omitempty"`
	Output       int  `json:"output_tokens,omitempty"`
	CacheRead    int  `json:"cache_read_tokens,omitempty"`
	CacheCreate  int  `json:"cache_creation_tokens,omitempty"`
}

// OutsideRun is an Event's Path for a tool path outside the run directory.
const OutsideRun = "<outside-run>"

// EventPath reports a tool's path for an Event: relative to the run directory,
// or OutsideRun for one outside it, with control characters replaced and at
// most 256 bytes. A CLI names absolute paths from its own working directory,
// which the kernel reports with symlinks resolved: on macOS the run directory
// under $TMPDIR (/var/folders/...) reaches the CLI as /private/var/folders/...,
// so a path under the resolved directory is inside the run too.
func EventPath(directory, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		relative, ok := within(directory, path)
		if !ok {
			if resolved, err := filepath.EvalSymlinks(directory); err == nil && resolved != directory {
				relative, ok = within(resolved, path)
			}
		}
		if !ok {
			return OutsideRun
		}
		path = relative
	}
	path = strings.Map(func(r rune) rune {
		if r < 32 {
			return '?'
		}
		return r
	}, path)
	return path[:min(len(path), 256)]
}

// within reports path relative to directory, and whether it is inside it.
func within(directory, path string) (string, bool) {
	relative, err := filepath.Rel(directory, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}
