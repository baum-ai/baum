package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/review"
)

// Generator is what a replay generates from a case. The runner builds the
// production options (assembler, backend, budgets, prompts, validator) and the
// generator runs them: ReviewGenerator is generate.Run, which writes the
// review document. Another kind of output plugs in by implementing this; the
// runner, the driver and the files it writes do not name the document's format.
type Generator interface {
	// Name is recorded in manifests and outcomes.
	Name() string
	Run(ctx context.Context, options generate.Options) (generate.Result, error)
}

// ReviewGenerator generates a pull request's review as the desktop app does.
type ReviewGenerator struct{}

func (ReviewGenerator) Name() string { return "review" }

func (ReviewGenerator) Run(ctx context.Context, o generate.Options) (generate.Result, error) {
	return generate.Run(ctx, o)
}

// Settings choose how a case is generated. The zero value of each field is the
// desktop app's default.
type Settings struct {
	// Backend is claude or codex; empty is claude. Model and Effort are empty
	// for the agent's own.
	Backend       config.Backend
	Model, Effort string
	// Staged writes the review in stages, as the desktop does.
	Staged bool
	// PromptOverrides replace built-in staged prompts, validated by
	// ValidateOverrides.
	PromptOverrides prompt.Overrides
	// TimeLimit bounds one case (the desktop's review time limit, ten minutes
	// by default); PassTimeout bounds one model call and is at least TimeLimit,
	// as in the desktop.
	TimeLimit, PassTimeout time.Duration
	// MaxCalls and MaxTokens bound a case; zero is no limit.
	MaxCalls, MaxTokens int
	// Caps override the default caps by name (config.CapNames).
	Caps map[string]int
	// Backends builds the backends; the zero value is the user's installed
	// agent CLIs, as the desktop runs them. Tests substitute fakes.
	Backends generate.Backends
	// Prices prices the run; nil is the active price list.
	Prices *pricing.List
	// Generator defaults to ReviewGenerator.
	Generator Generator
	// OutputDir, when set, receives the case's files: document.json (when
	// one was generated), outcome.json, diagnostics.json, progress.log and
	// transcript.jsonl. It must exist.
	OutputDir string
	// NoTranscript leaves transcript.jsonl out.
	NoTranscript bool
}

// ValidateOverrides checks the override texts against the staged review
// prompt ids and rejects them for a run that is not staged.
func (s Settings) ValidateOverrides() error {
	for id, text := range s.PromptOverrides {
		if !s.Staged {
			return fmt.Errorf("prompt override %s needs a staged run: a review written in one pass has no replaceable prompt", id)
		}
		known := false
		for _, review := range prompt.ReviewIDs(false, true) {
			known = known || review == id
		}
		if !known {
			return fmt.Errorf("prompt override %q is not a staged review prompt; use one of %v", id, prompt.ReviewIDs(false, true))
		}
		if err := prompt.Validate(id, text); err != nil {
			return fmt.Errorf("prompt override %s: %w", id, err)
		}
	}
	return nil
}

// Call is one model call of a case.
type Call struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

// Outcome is what one case's replay produced. Document is kept out of the
// JSON: it is written as its own file.
type Outcome struct {
	CaseID    string `json:"case_id"`
	Generator string `json:"generator"`
	// Status is "ok" or "failed". Reason is a generate.Category, or "case"
	// for an input that did not load, and Error the redacted message.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`

	StartedAt   time.Time `json:"started_at"`
	WallSeconds float64   `json:"wall_seconds"`

	Backend        string `json:"backend,omitempty"`
	BackendVersion string `json:"backend_version,omitempty"`
	Model          string `json:"model,omitempty"`
	Staged         bool   `json:"staged"`
	PromptVersion  string `json:"prompt_version,omitempty"`
	PromptDigest   string `json:"prompt_digest,omitempty"`
	BundleDigest   string `json:"bundle_digest,omitempty"`
	SchemaRevision string `json:"schema_revision,omitempty"`

	Calls     int               `json:"calls"`
	Tokens    int               `json:"tokens"`
	Usage     agent.Usage       `json:"usage"`
	CostUSD   *float64          `json:"cost_usd"`
	Repairs   int               `json:"repairs"`
	Accounts  *agent.Accounting `json:"accounting,omitempty"`
	CallLog   []Call            `json:"call_log,omitempty"`
	Findings  string            `json:"findings,omitempty"`
	Failed    []string          `json:"failed_sections,omitempty"`
	NoContext bool              `json:"context_failed,omitempty"`
	// StoredValid says the document passes the stored-document validator
	// against the case's own bundle.
	StoredValid    bool     `json:"stored_valid"`
	StoredFindings []string `json:"stored_findings,omitempty"`

	Document []byte `json:"-"`
}

// OK reports a case that produced a valid document.
func (o Outcome) OK() bool { return o.Status == "ok" }

// Runner generates cases with fixed settings. It is safe for concurrent use.
type Runner struct {
	Settings Settings
	Selected generate.Selected
	// Caps are the effective caps.
	Caps      map[string]int
	validator *review.DocumentValidator
	prices    *pricing.List
	generator Generator
	config    config.Config
}

// NewRunner selects and checks the backend (it must be installed and logged in)
// and validates the settings.
func NewRunner(ctx context.Context, s Settings) (*Runner, error) {
	if err := s.ValidateOverrides(); err != nil {
		return nil, err
	}
	cfg := config.Default()
	cfg.Backend, cfg.Model, cfg.Effort = s.Backend, s.Model, s.Effort
	if cfg.Backend == "" {
		cfg.Backend = config.BackendClaude
	}
	if !config.ValidEffort(s.Effort) {
		return nil, fmt.Errorf("unknown reasoning effort %q", s.Effort)
	}
	cfg.Caps = s.Caps
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return nil, err
	}
	if s.TimeLimit <= 0 {
		s.TimeLimit = time.Duration(cfg.Budgets.ReviewTimeout)
	}
	if s.PassTimeout <= 0 {
		s.PassTimeout = time.Duration(cfg.Budgets.PassTimeout)
	}
	if s.Backends.Claude == nil && s.Backends.Codex == nil {
		s.Backends = generate.DefaultBackendsFor(cfg)
	}
	selected, err := generate.SelectDesktop(ctx, cfg.Backend, cfg.Model, s.Backends, io.Discard)
	if err != nil {
		return nil, err
	}
	validator, err := review.NewDocumentValidator()
	if err != nil {
		return nil, err
	}
	r := &Runner{Settings: s, Selected: selected, Caps: caps, validator: validator, prices: s.Prices, generator: s.Generator, config: cfg}
	if r.prices == nil {
		r.prices = pricing.Active()
	}
	if r.generator == nil {
		r.generator = ReviewGenerator{}
	}
	return r, nil
}

// Prompts describes the prompts a run sends: their version and digest, and
// the override texts that replace built-in ones.
func (r *Runner) Prompts(ctx context.Context) (version, digest string, err error) {
	if !r.Settings.Staged {
		instructions, err := prompt.Prompts{}.Instructions(ctx, r.Selected.Backend.Name())
		return instructions.Version, instructions.Digest, err
	}
	instructions, err := prompt.StagedPrompts{Overrides: r.Settings.PromptOverrides}.StagedInstructions(ctx, r.Selected.Backend.Name())
	return instructions.Version, instructions.Digest, err
}

// Run generates one case. The error is for a replay that could not be
// attempted; a generation that failed is an Outcome with Status "failed".
// With Settings.OutputDir set the case's files are written there.
func (r *Runner) Run(ctx context.Context, c Case) (Outcome, error) {
	s := r.Settings
	started := time.Now()
	out := Outcome{CaseID: c.ID, Generator: r.generator.Name(), StartedAt: started.UTC(), Backend: r.Selected.Backend.Name(), BackendVersion: r.Selected.Version,
		Model: r.Selected.Model, Staged: s.Staged, BundleDigest: c.Digest}
	// The progress log is written as the run goes, so a long case can be watched.
	progress := &syncWriter{}
	if s.OutputDir != "" {
		file, err := os.Create(filepath.Join(s.OutputDir, "progress.log"))
		if err != nil {
			return out, err
		}
		defer func() { _ = file.Close() }()
		progress.w = file
	}
	tracked := &trackedBackend{CheckedBackend: r.Selected.Backend, keep: !s.NoTranscript && s.OutputDir != ""}
	diagnostics := ""
	if s.OutputDir != "" {
		diagnostics = filepath.Join(s.OutputDir, "diagnostics.json")
	}
	options := generate.Options{
		PriceList: r.prices, Assembler: Assembler{Case: c},
		Request: assemble.Request{Repository: c.Repository, Number: c.Bundle.PullRequest.Number, Limits: assemble.LimitsFrom(r.Caps)},
		Backend: tracked, BackendVersion: r.Selected.Version, Model: r.Selected.Model,
		Budgets:     agent.Budgets{WallClock: s.TimeLimit, MaxCalls: s.MaxCalls, MaxTokens: s.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 2},
		PassTimeout: max(s.PassTimeout, s.TimeLimit),
		Staged:      s.Staged, Validator: r.validator, Progress: progress, DiagnosticsOut: diagnostics,
	}
	if s.Staged {
		options.StagedPrompts = prompt.StagedPrompts{Overrides: s.PromptOverrides}
	}
	result, err := r.generator.Run(ctx, options)
	out.WallSeconds = time.Since(started).Seconds()
	out.Calls, out.Tokens, out.Usage = result.Outcome.Calls, result.Outcome.Tokens, result.Outcome.Usage
	out.Repairs, out.Accounts, out.Findings = result.Outcome.ValidationRepairs, result.Outcome.Accounting, result.Outcome.Findings
	out.SchemaRevision = result.Outcome.SchemaRevision
	if model := result.Outcome.Model; model != "" {
		out.Model = model
	}
	out.CostUSD = r.prices.BackendOutcomeCost(out.Backend, out.Model, result.Outcome)
	out.CallLog = tracked.calls()
	if err != nil {
		out.Status, out.Reason, out.Error = "failed", string(generate.CategoryOf(err)), err.Error()
		if out.Reason == "" {
			out.Reason = string(generate.CategoryInternal)
		}
	} else {
		out.Status = "ok"
		out.Document = result.Document
		out.PromptVersion, out.PromptDigest = result.Metadata.PromptVersion, result.Metadata.PromptDigest
		out.Failed, out.NoContext = result.FailedSections, result.ContextFailed
		b, err := c.FreshBundle()
		if err == nil {
			out.StoredFindings = r.validator.ValidateStored(result.Document, &b)
		}
		out.StoredValid = err == nil && len(out.StoredFindings) == 0
		if !out.StoredValid {
			out.Status, out.Reason = "failed", string(generate.CategoryValidation)
			out.Error = "the document does not pass the stored-document validator"
		}
	}
	if s.OutputDir != "" {
		if werr := writeCase(s.OutputDir, out, tracked.transcript()); werr != nil {
			return out, werr
		}
	}
	return out, nil
}

func writeCase(dir string, out Outcome, transcript []byte) error {
	if out.Document != nil {
		if err := generate.WriteFileAtomic(filepath.Join(dir, "document.json"), out.Document); err != nil {
			return err
		}
	}
	if len(transcript) > 0 {
		if err := generate.WriteFileAtomic(filepath.Join(dir, "transcript.jsonl"), transcript); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	// Written last: a case without outcome.json did not finish.
	return generate.WriteFileAtomic(filepath.Join(dir, "outcome.json"), append(encoded, '\n'))
}

// ReadOutcome reads a case's outcome.json from a run's case directory.
func ReadOutcome(dir string) (Outcome, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "outcome.json"))
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome
	if err := json.Unmarshal(raw, &out); err != nil {
		return Outcome{}, fmt.Errorf("%s: %w", dir, err)
	}
	return out, nil
}

// trackedBackend records each model call's name, time and error, and, when
// keep is set, every step the agent took as JSON lines.
type trackedBackend struct {
	generate.CheckedBackend
	keep bool

	n    atomic.Int64
	mu   sync.Mutex
	log  []Call
	text bytes.Buffer
}

// ConcurrentCalls forwards the backend's agent.ConcurrentCaller, so a staged
// review runs its details and context at once where the backend allows.
func (b *trackedBackend) ConcurrentCalls() bool { return generate.ConcurrentCalls(b.CheckedBackend) }

func (b *trackedBackend) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	id := b.n.Add(1)
	name := fmt.Sprintf("call-%d %s pass %d", id, r.Stage, r.Pass)
	if b.keep && r.Transcript == nil {
		r.Transcript = func(entry agent.TranscriptEntry) {
			entry.Call = name
			encoded, err := json.Marshal(entry)
			if err != nil {
				return
			}
			b.mu.Lock()
			b.text.Write(append(encoded, '\n'))
			b.mu.Unlock()
		}
	}
	started := time.Now()
	result, err := b.CheckedBackend.Run(ctx, r)
	call := Call{Name: name, Seconds: time.Since(started).Seconds()}
	if err != nil {
		call.Error = truncate(err.Error(), 300)
	}
	b.mu.Lock()
	b.log = append(b.log, call)
	b.mu.Unlock()
	return result, err
}

func (b *trackedBackend) calls() []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Call(nil), b.log...)
}

func (b *trackedBackend) transcript() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.text.Bytes()...)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

// syncWriter serialises the pipeline's progress lines, which stages write
// from their own goroutines; a nil writer discards them.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return len(p), nil
	}
	return s.w.Write(p)
}
