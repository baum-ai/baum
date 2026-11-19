package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
)

// Generator is what a replay generates from a case, and everything about it
// that depends on the document's format. The runner builds the production
// options that do not (assembler, backend, budgets, accounting) and hands them
// to the generator, so another kind of output implements this interface and
// nothing else of the runner, driver, guards, files or summary changes.
type Generator interface {
	// Name is recorded in manifests and outcomes.
	Name() string
	// Describe reports what a run with this generator sends, for the manifest:
	// its own settings and the versions and digests of its prompts. A resume
	// refuses to continue a run whose description differs.
	Describe(ctx context.Context, backend string) (map[string]any, error)
	// Generate runs the pipeline for one case. base holds the options common to
	// every generator; the result is returned even when err is not nil, with
	// whatever the run spent.
	Generate(ctx context.Context, base generate.Options, c Case) (Generated, error)
	// Validate checks a generated document against its case and returns the
	// findings, none for a valid document.
	Validate(document []byte, c Case) []string
}

// GeneratedValidator is implemented by a generator whose validation needs the
// artifacts it wrote beside the document: the runner then calls
// ValidateGenerated instead of Validate.
type GeneratedValidator interface {
	ValidateGenerated(g Generated, c Case) []string
}

// Chainer is implemented by a generator whose document can be the previous
// version of the next revision of a pull request, which a chain run needs.
type Chainer interface {
	// Next returns c's bundle with document, which the generator produced for an
	// earlier revision of the pull request, carried as its previous version the
	// way the app carries a stored version into the next generation (the version
	// number is the document's own). The runner then masks and fits the bundle
	// as assembly does (Case.Refit).
	Next(c Case, document []byte) (bundle.Bundle, error)
}

// Generated is what a generator produced for one case.
type Generated struct {
	Document []byte
	Outcome  agent.Outcome
	// PromptVersion and PromptDigest identify the prompt actually sent.
	PromptVersion, PromptDigest string
	// BundleDigest is the digest of the bundle the run read.
	BundleDigest string
	// Notes are short labels for the summary table (such as parts that failed);
	// Details is format-specific and kept in outcome.json as it is.
	Notes   []string
	Details map[string]any
	// Artifacts are extra named files, written beside the document in the
	// case's directory and listed in the outcome. A name is a relative path
	// below the case directory; it cannot be one of the runner's own files.
	Artifacts map[string][]byte
}

// Settings choose how a case is generated. The zero value of each field is the
// desktop app's default, except that the backend is claude and the agent's
// environment is what the process has: replay reads neither the developer's
// baum configuration nor the desktop's preferences.
type Settings struct {
	// Backend is claude or codex; empty is claude. Model and Effort are empty
	// for the agent's own.
	Backend       config.Backend
	Model, Effort string
	// ClaudeConfigDir and CodexHome, and AgentEnv, are the config fields the
	// app sets for its agents (config.Config.ClaudeConfigDir, CodexHome,
	// AgentEnv); empty leaves the ambient login.
	ClaudeConfigDir, CodexHome string
	AgentEnv                   map[string]string
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
	// Generator defaults to a staged ReviewGenerator.
	Generator Generator
	// OutputDir, when set, receives the case's files: document.json (when
	// one was generated), outcome.json, diagnostics.json, progress.log and
	// transcript.jsonl. It must exist.
	OutputDir string
	// NoTranscript leaves transcript.jsonl out.
	NoTranscript bool
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
	// Status is "ok", "failed" or "interrupted". Reason is a generate.Category,
	// or "case" for an input that did not load, and Error the redacted message.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`

	StartedAt   time.Time `json:"started_at"`
	WallSeconds float64   `json:"wall_seconds"`

	Backend        string `json:"backend,omitempty"`
	BackendVersion string `json:"backend_version,omitempty"`
	Model          string `json:"model,omitempty"`
	PromptVersion  string `json:"prompt_version,omitempty"`
	PromptDigest   string `json:"prompt_digest,omitempty"`
	BundleDigest   string `json:"bundle_digest,omitempty"`
	// BundleBytes is the encoded size of the bundle the model read. BundleFitted
	// says that a chain fitted the bundle to the evidence budget and cut
	// something, and BundleNotes are the notes that cut recorded.
	BundleBytes    int      `json:"bundle_bytes,omitempty"`
	BundleFitted   bool     `json:"bundle_fitted,omitempty"`
	BundleNotes    []string `json:"bundle_notes,omitempty"`
	SchemaRevision string   `json:"schema_revision,omitempty"`

	Calls    int               `json:"calls"`
	Tokens   int               `json:"tokens"`
	Usage    agent.Usage       `json:"usage"`
	CostUSD  *float64          `json:"cost_usd"`
	Repairs  int               `json:"repairs"`
	Accounts *agent.Accounting `json:"accounting,omitempty"`
	CallLog  []Call            `json:"call_log,omitempty"`
	Findings string            `json:"findings,omitempty"`
	// Artifacts lists the extra files the generator wrote beside the document.
	Artifacts []string       `json:"artifacts,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	// DocumentValid says the document passes the generator's validation
	// against the case; ValidationFindings are what it found otherwise.
	DocumentValid      bool     `json:"document_valid"`
	ValidationFindings []string `json:"validation_findings,omitempty"`

	Document []byte `json:"-"`
	// Extra holds the artifacts' content.
	Extra map[string][]byte `json:"-"`
}

// OK reports a case that produced a valid document.
func (o Outcome) OK() bool { return o.Status == "ok" }

// Runner generates cases with fixed settings. It is safe for concurrent use.
type Runner struct {
	Settings Settings
	Selected generate.Selected
	// Caps are the effective caps.
	Caps map[string]int
	// Described is the generator's description (Generator.Describe).
	Described map[string]any
	prices    *pricing.List
	generator Generator
}

// NewRunner selects and checks the backend (it must be installed and logged in)
// and the generator's settings.
func NewRunner(ctx context.Context, s Settings) (*Runner, error) {
	cfg := config.Default()
	cfg.Backend, cfg.Model, cfg.Effort = s.Backend, s.Model, s.Effort
	if cfg.Backend == "" {
		cfg.Backend = config.BackendClaude
	}
	if !config.ValidEffort(s.Effort) {
		return nil, fmt.Errorf("unknown reasoning effort %q", s.Effort)
	}
	if err := config.ValidateAgentEnv(s.AgentEnv); err != nil {
		return nil, fmt.Errorf("agent environment: %w", err)
	}
	cfg.Caps, cfg.AgentEnv, cfg.ClaudeConfigDir, cfg.CodexHome = s.Caps, s.AgentEnv, s.ClaudeConfigDir, s.CodexHome
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
	if s.Generator == nil {
		s.Generator = &ReviewGenerator{Staged: true}
	}
	if s.Backends.Claude == nil && s.Backends.Codex == nil {
		s.Backends = generate.DefaultBackendsFor(cfg)
	}
	selected, err := generate.SelectDesktop(ctx, cfg.Backend, cfg.Model, s.Backends, io.Discard)
	if err != nil {
		return nil, err
	}
	described, err := s.Generator.Describe(ctx, selected.Backend.Name())
	if err != nil {
		return nil, err
	}
	r := &Runner{Settings: s, Selected: selected, Caps: caps, Described: described, prices: s.Prices, generator: s.Generator}
	if r.prices == nil {
		r.prices = pricing.Active()
	}
	return r, nil
}

// Run generates one case. The error is for a replay that could not be
// attempted; a generation that failed is an Outcome with Status "failed", and
// one stopped by ctx has Status "interrupted" and leaves no files.
// With Settings.OutputDir set the case's files are written there.
func (r *Runner) Run(ctx context.Context, c Case) (Outcome, error) {
	s := r.Settings
	started := time.Now()
	out := Outcome{CaseID: c.ID, Generator: r.generator.Name(), StartedAt: started.UTC(), Backend: r.Selected.Backend.Name(), BackendVersion: r.Selected.Version,
		Model: r.Selected.Model, BundleDigest: c.Digest, BundleBytes: len(c.BundleJSON), BundleFitted: c.Fitted, BundleNotes: c.FitNotes}
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
	base := generate.Options{
		PriceList: r.prices, Assembler: Assembler{Case: c},
		Request: assemble.Request{Repository: c.Repository, Number: c.Bundle.PullRequest.Number, Limits: assemble.LimitsFrom(r.Caps)},
		Backend: tracked, BackendVersion: r.Selected.Version, Model: r.Selected.Model,
		Budgets:     agent.Budgets{WallClock: s.TimeLimit, MaxCalls: s.MaxCalls, MaxTokens: s.MaxTokens, MaxProviderAttempts: 3, MaxRepairAttempts: 2},
		PassTimeout: max(s.PassTimeout, s.TimeLimit),
		Progress:    progress, DiagnosticsOut: diagnostics,
	}
	generated, err := r.generator.Generate(ctx, base, c)
	out.WallSeconds = time.Since(started).Seconds()
	spent := generated.Outcome
	out.Calls, out.Tokens, out.Usage = spent.Calls, spent.Tokens, spent.Usage
	out.Repairs, out.Accounts, out.Findings = spent.ValidationRepairs, spent.Accounting, spent.Findings
	out.SchemaRevision = spent.SchemaRevision
	if model := spent.Model; model != "" {
		out.Model = model
	}
	out.CostUSD = r.prices.BackendOutcomeCost(out.Backend, out.Model, spent)
	out.CallLog = tracked.calls()
	if badArtifacts := checkArtifacts(generated.Artifacts); badArtifacts != nil {
		if err == nil {
			err = badArtifacts
		}
	} else if len(generated.Artifacts) > 0 {
		out.Extra = generated.Artifacts
		out.Artifacts = slices.Sorted(maps.Keys(generated.Artifacts))
	}
	switch {
	case err != nil:
		out.Status, out.Reason, out.Error = "failed", string(generate.CategoryOf(err)), err.Error()
		if out.Reason == "" {
			out.Reason = string(generate.CategoryInternal)
		}
		if out.Reason == string(generate.CategoryInterrupted) {
			out.Status = "interrupted"
		}
	default:
		out.Status = "ok"
		out.Document = generated.Document
		out.PromptVersion, out.PromptDigest = generated.PromptVersion, generated.PromptDigest
		if generated.BundleDigest != "" {
			out.BundleDigest = generated.BundleDigest
		}
		out.Notes, out.Details = generated.Notes, generated.Details
		if v, ok := r.generator.(GeneratedValidator); ok {
			out.ValidationFindings = v.ValidateGenerated(generated, c)
		} else {
			out.ValidationFindings = r.generator.Validate(generated.Document, c)
		}
		out.DocumentValid = len(out.ValidationFindings) == 0
		if !out.DocumentValid {
			out.Status, out.Reason = "failed", string(generate.CategoryValidation)
			out.Error = "the document does not pass the generator's validation"
		}
	}
	if s.OutputDir != "" {
		if out.Status == "interrupted" {
			for _, name := range caseFiles {
				_ = os.Remove(filepath.Join(s.OutputDir, name))
			}
			return out, nil
		}
		if werr := writeCase(s.OutputDir, out, tracked.transcript()); werr != nil {
			return out, werr
		}
	}
	return out, nil
}

// caseFiles are the files a case's run writes.
var caseFiles = []string{"document.json", "transcript.jsonl", "progress.log", "diagnostics.json", "outcome.json"}

// checkArtifacts refuses a name that leaves the case directory, one that is
// (ignoring case, as some file systems do) one of the runner's files or
// another artifact's name, and one that is both a file and a directory that
// holds another artifact.
func checkArtifacts(artifacts map[string][]byte) error {
	seen := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(artifacts)) {
		clean := strings.ToLower(filepath.ToSlash(filepath.Clean(name)))
		if !filepath.IsLocal(name) || strings.ContainsAny(name, `\`) || slices.Contains(caseFiles, clean) {
			return fmt.Errorf("the generator's artifact name %q is not a file name below the case directory, or it is one of the runner's own files", name)
		}
		if other, ok := seen[clean]; ok {
			return fmt.Errorf("the generator's artifact names %q and %q are the same file", other, name)
		}
		seen[clean] = name
	}
	for clean, name := range seen {
		for other, otherName := range seen {
			if strings.HasPrefix(other, clean+"/") {
				return fmt.Errorf("the generator's artifact %q is a file and also a directory that holds %q", name, otherName)
			}
		}
	}
	return nil
}

func writeCase(dir string, out Outcome, transcript []byte) error {
	if out.Document != nil {
		if err := generate.WriteFileAtomic(filepath.Join(dir, "document.json"), out.Document); err != nil {
			return err
		}
	}
	for name, content := range out.Extra {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := generate.WriteFileAtomic(path, content); err != nil {
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

// KeepsSessions forwards the backend's agent.SessionKeeper, so the overview
// resumes the map's session where the backend allows.
func (b *trackedBackend) KeepsSessions() bool { return agent.KeepsSessions(b.CheckedBackend) }

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
