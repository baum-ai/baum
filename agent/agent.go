// Package agent defines the model backend interface and the types a run
// exchanges with it: the request, the result, the usage and the event log, plus
// the run budgets, the failure reasons, error classification and the
// environment a local CLI backend's child process receives.
package agent

import (
	"context"
	"log/slog"
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
