// Package generate is the pipeline behind "baum generate": it assembles the
// generation bundle for a pull request, unpacks the checkout into a private
// run directory, runs the shared generate-and-repair loop (agent.Generator)
// with a model backend, and finishes the document the way the hosted worker
// does: output redaction, secret rejection, derived fields, the generator
// block and a strict check of the stored form.
//
// Its sources and the backend are interfaces, so a test runs the whole
// pipeline against a fake clone, a fake GitHub and a stub backend. Run never
// writes the document anywhere: it returns it, and nothing is returned for a
// run that was rejected, so a caller cannot publish a partial result.
//
// Every failure is an *Error naming a Category, which the command line maps to
// an exit code.
package generate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/sessions"
)

// Category classifies why a run failed.
type Category string

const (
	// CategoryInput is a failure of what the run needs: the pull request, its
	// commits in the clone, GitHub, or a bundle or checkout over its caps.
	CategoryInput Category = "input"
	// CategoryBackend is a backend that is missing, too old, cannot sandbox,
	// failed or whose provider was unavailable.
	CategoryBackend Category = "backend"
	// CategoryBudget is a spent call, token or wall-clock budget.
	CategoryBudget Category = "budget"
	// CategoryValidation is a document the validator rejected after the
	// repairs, or one that held a secret. Nothing is written for it.
	CategoryValidation Category = "validation"
	// CategoryInterrupted is a run stopped by SIGINT or SIGTERM.
	CategoryInterrupted Category = "interrupted"
	// CategoryInternal is a failure of baum's own steps, such as masking the
	// output. The hosted worker treats it as a plain failure, not a rejected
	// document, so it exits with the generic code.
	CategoryInternal Category = "internal"
)

// Error is a failure of one category. Its text never holds a secret the model
// or the backend wrote: it is redacted before it is built.
type Error struct {
	Category Category
	Err      error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// CategoryOf returns the category of err, or "" when it is not an *Error.
func CategoryOf(err error) Category {
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return ""
}

// Assembler builds the bundle and the archives of one pull request;
// *assemble.Assembler implements it.
type Assembler interface {
	Assemble(ctx context.Context, req assemble.Request) (assemble.Result, error)
}

// Options are the inputs of one run.
type Options struct {
	Assembler Assembler
	// Request names the pull request and the limits. Run sets its Checkout and
	// Attachments writers.
	Request assemble.Request
	Backend agent.Backend
	// BackendVersion is the installed backend's version, for the progress line.
	BackendVersion string
	Model          string
	Budgets        agent.Budgets
	// PassTimeout bounds each backend call; zero leaves it to the wall clock.
	PassTimeout time.Duration
	// TileKinds is the tile allow-list; empty offers no tiles.
	TileKinds []string
	// Prompts defaults to this module's public prompt.
	Prompts   agent.Prompts
	Validator *review.DocumentValidator
	Redactor  *bundle.Redactor
	// Progress receives one line per step; nil discards them. The document is
	// never written to it.
	Progress io.Writer
	// KeepRunDir keeps the run directory and reports its path on Progress.
	KeepRunDir bool
	// BundleOut, when not empty, receives the exact bundle the model was given.
	BundleOut string
	// DiagnosticsOut, when not empty, receives the run's redacted outcome as
	// JSON: usage, repairs, findings and at most agent.MaxDiagnostics events.
	// It is written for failed runs too.
	DiagnosticsOut string
	// Now defaults to time.Now.
	Now func() time.Time
}

// Result is a finished run.
type Result struct {
	// Document is the finished, strictly valid document. Nil on any failure.
	Document []byte
	Outcome  agent.Outcome
	// RunDir is the run directory when KeepRunDir kept it.
	RunDir string
	// Elapsed is the wall time of the run.
	Elapsed time.Duration
}

// Run executes the pipeline. The run directory is removed on every path
// unless Options.KeepRunDir is set.
func Run(ctx context.Context, o Options) (result Result, err error) {
	started := time.Now()
	defer func() { result.Elapsed = time.Since(started) }()
	// Written for every outcome, early failures included.
	defer func() {
		if o.DiagnosticsOut != "" {
			if writeErr := WriteDiagnostics(o.DiagnosticsOut, err, result.Outcome); writeErr != nil && o.Progress != nil {
				_, _ = fmt.Fprintf(o.Progress, "could not write the diagnostics: %v\n", writeErr)
			}
		}
	}()
	progress := func(format string, args ...any) {
		if o.Progress != nil {
			_, _ = fmt.Fprintf(o.Progress, format+"\n", args...)
		}
	}
	validator := o.Validator
	if validator == nil {
		if validator, err = review.NewDocumentValidator(); err != nil {
			return result, err
		}
	}
	redactor := o.Redactor
	if redactor == nil {
		if redactor, err = bundle.NewRedactor(); err != nil {
			return result, err
		}
	}
	prompts := o.Prompts
	if prompts == nil {
		prompts = prompt.Prompts{}
	}
	prompts = markedPrompts{prompts}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	// fail builds the error of a category with its text redacted.
	fail := func(category Category, err error) error {
		text := err.Error()
		if masked, _, maskErr := redactor.RedactString(context.WithoutCancel(ctx), text); maskErr == nil {
			text = masked
		}
		return &Error{Category: category, Err: errors.New(text)}
	}
	interrupted := func(err error) error {
		if ctx.Err() != nil {
			return &Error{Category: CategoryInterrupted, Err: errors.New("interrupted")}
		}
		return err
	}

	prompted, err := prompts.Instructions(ctx, o.Backend.Name())
	if err != nil {
		return result, fail(CategoryBackend, err)
	}

	archives, err := os.MkdirTemp("", "baum-generate-*")
	if err != nil {
		return result, err
	}
	defer removeTree(archives)
	runDir, err := os.MkdirTemp("", agent.RunDirPrefix+"*")
	if err != nil {
		return result, err
	}
	if o.KeepRunDir {
		result.RunDir = runDir
		defer progress("run directory kept: %s", runDir)
	} else {
		defer removeTree(runDir)
	}

	request := o.Request
	progress("assembling the bundle for %s#%d", request.Repository, request.Number)
	checkoutPath, attachmentsPath := filepath.Join(archives, "checkout.tar.gz"), filepath.Join(archives, "attachments.tar")
	checkoutFile, err := os.OpenFile(checkoutPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result, err
	}
	defer func() { _ = checkoutFile.Close() }()
	attachmentsFile, err := os.OpenFile(attachmentsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result, err
	}
	defer func() { _ = attachmentsFile.Close() }()
	request.Checkout, request.Attachments = checkoutFile, attachmentsFile
	assembled, err := o.Assembler.Assemble(ctx, request)
	if err != nil {
		err = interrupted(err)
		if CategoryOf(err) != "" {
			return result, err
		}
		if errors.Is(err, github.ErrNotFound) {
			err = fmt.Errorf("pull request %s#%d was not found: it does not exist, or your token cannot see it: %w", request.Repository, request.Number, err)
		}
		return result, fail(CategoryInput, err)
	}
	if err := errors.Join(checkoutFile.Close(), attachmentsFile.Close()); err != nil {
		return result, err
	}
	b := assembled.Bundle
	progress("bundle sha256:%s: %d hunk(s), %d session(s), %d masked span(s)", b.Digest, len(b.Diff), len(b.Sessions), b.MaskedSpanCount)
	if o.BundleOut != "" {
		if err := WriteFileAtomic(o.BundleOut, assembled.JSON); err != nil {
			return result, fmt.Errorf("writing the bundle: %w", err)
		}
	}
	limits := request.Limits
	if err := bundle.UnpackCheckout(ctx, checkoutPath, runDir, limits.Checkout); err != nil {
		if err = interrupted(err); CategoryOf(err) != "" {
			return result, err
		}
		return result, fail(CategoryInput, fmt.Errorf("unpacking the checkout: %w", err))
	}
	if len(b.Attachments) > 0 {
		if err := bundle.UnpackAttachments(ctx, attachmentsPath, runDir, b.Attachments, limits.MaxImageBytes); err != nil {
			if err = interrupted(err); CategoryOf(err) != "" {
				return result, err
			}
			return result, fail(CategoryInput, fmt.Errorf("unpacking the description images: %w", err))
		}
	}
	// A path the checkout holds may stay in the returned document as it is.
	checkout := checkoutPaths(runDir, limits.Checkout.MaxTotalFiles)

	label := o.Backend.Name()
	if o.BackendVersion != "" {
		label += " " + o.BackendVersion
	}
	if o.Model != "" {
		label += ", model " + o.Model
	}
	progress("running %s", label)
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if o.Budgets.WallClock > 0 {
		runCtx, cancel = context.WithTimeout(ctx, o.Budgets.WallClock)
	}
	defer cancel()
	backend := o.Backend
	if o.PassTimeout > 0 {
		backend = passBounded{Backend: backend, timeout: o.PassTimeout}
	}
	generator := agent.Generator{
		Backend: backend, Prompts: prompts, Validator: validator, Redactor: redactor,
		Logger: slog.New(progressHandler{w: o.Progress}),
	}
	outcome, document, genErr := generator.Generate(runCtx, agent.Run{
		Bundle: &b, EncodedBundle: assembled.JSON, Directory: runDir, TileKinds: o.TileKinds, Budgets: o.Budgets,
	})
	// What the loop returned is masked on every path, as the hosted worker
	// does, before any of it is shown, written or put in an error.
	redactCtx := context.WithoutCancel(ctx)
	document, closed, redactErr := agent.RedactOutput(redactCtx, validator, redactor, document, &b, checkout, &outcome)
	if redactErr == nil {
		document, _ = agent.RejectSecrets(&outcome, document, closed)
	}
	result.Outcome = outcome
	defer func() {
		if err == nil {
			progress("generated in %s: %s, %d repair(s)", time.Since(started).Round(time.Second), spent(outcome), outcome.ValidationRepairs)
		} else if outcome.Calls > 0 || outcome.Tokens > 0 {
			progress("spent before failing: %s, %d repair(s)", spent(outcome), outcome.ValidationRepairs)
		}
	}()

	if genErr != nil {
		category, cause := classify(ctx, runCtx, genErr)
		return result, fail(category, cause)
	}
	if redactErr != nil {
		return result, fail(CategoryInternal, fmt.Errorf("masking the output: %w", redactErr))
	}
	if outcome.Reason == agent.ReasonValidationRejected {
		return result, rejection(fail, outcome.Findings)
	}
	if err := ctx.Err(); err != nil {
		return result, interrupted(err)
	}
	completed, findings := validator.CompleteWithPrompt(document, &b, runDir, outcome.Model, prompted.Version, now())
	if findings != "" {
		outcome.Reason, outcome.Findings = agent.ReasonValidationRejected, findings
		result.Outcome = outcome
		return result, rejection(fail, findings)
	}
	// CompleteWithPrompt already validates the full document, so this strict
	// check of the stored form is defence in depth, as the hosted worker's
	// storage path is: no document the model can write reaches it, because the
	// host normalises line ranges and unresolved anchors before it.
	if stored := validator.ValidateStored(completed, &b); len(stored) > 0 {
		outcome.Reason, outcome.Findings = agent.ReasonValidationRejected, strings.Join(stored, "\n")
		result.Outcome = outcome
		return result, rejection(fail, outcome.Findings)
	}
	result.Document = completed
	return result, nil
}

// rejection is the error of a rejected document: the reason and the first of
// its findings, so a terminal never fills with a long list.
func rejection(fail func(Category, error) error, findings string) error {
	const maxLines, maxBytes = 12, 4096
	lines := strings.Split(strings.TrimSpace(findings), "\n")
	more := 0
	if len(lines) > maxLines {
		more, lines = len(lines)-maxLines, lines[:maxLines]
	}
	text := strings.Join(lines, "\n  ")
	if len(text) > maxBytes {
		text = strings.ToValidUTF8(text[:maxBytes], "") + "..."
	}
	if more > 0 {
		text += fmt.Sprintf("\n  ... and %d more finding(s); use --diagnostics for all of them", more)
	}
	return fail(CategoryValidation, fmt.Errorf("%s:\n  %s", agent.ReasonValidationRejected, text))
}

// classify maps a failure of the loop to its category and the error to report:
// an interrupted run says so, and a spent wall clock names that budget, as the
// hosted worker does.
func classify(parent, run context.Context, err error) (Category, error) {
	text := err.Error()
	switch {
	case parent.Err() != nil:
		return CategoryInterrupted, errors.New("interrupted")
	case errors.Is(run.Err(), context.DeadlineExceeded):
		return CategoryBudget, errors.New(agent.GenerationBudgetExceededReason("wall_clock"))
	case strings.HasPrefix(text, agent.GenerationBudgetExceededReason("")):
		return CategoryBudget, err
	default:
		return CategoryBackend, err
	}
}

func spent(outcome agent.Outcome) string {
	if outcome.Usage.Unreported || (outcome.Tokens == 0 && outcome.Usage.IsZero()) {
		return fmt.Sprintf("%d call(s), usage not reported", outcome.Calls)
	}
	return fmt.Sprintf("%d call(s), %d token(s)", outcome.Calls, outcome.Tokens)
}

// markedPrompts starts every prompt with sessions.GenerationRunMarker, which is
// how session evidence recognises, and leaves out, the sessions of baum's own
// runs: a CLI backend records its run in the developer's own session stores.
// The loop starts every prompt it sends, repairs included, with the body.
type markedPrompts struct{ agent.Prompts }

func (p markedPrompts) Instructions(ctx context.Context, backend string) (agent.Prompt, error) {
	prompt, err := p.Prompts.Instructions(ctx, backend)
	if err != nil {
		return prompt, err
	}
	prompt.Body = sessions.GenerationRunMarker + "\n" + prompt.Body
	// Digest stays the sha256 of Body, which is what is sent.
	sum := sha256.Sum256([]byte(prompt.Body))
	prompt.Digest = hex.EncodeToString(sum[:])
	return prompt, nil
}

// passBounded applies the per-call timeout to a backend.
type passBounded struct {
	agent.Backend
	timeout time.Duration
}

func (p passBounded) Run(ctx context.Context, request agent.Request) (agent.Result, error) {
	pass, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	result, err := p.Backend.Run(pass, request)
	if err != nil && ctx.Err() == nil && errors.Is(pass.Err(), context.DeadlineExceeded) {
		// The pass's own deadline fired, not the run's or the user's: a spent
		// budget, which the loop does not retry.
		return result, errors.New(agent.GenerationBudgetExceededReason("pass_timeout"))
	}
	return result, err
}

// checkoutPaths lists the repository paths of the regular files of the
// unpacked head and merge-base trees.
func checkoutPaths(dir string, limit int) map[string]bool {
	paths := map[string]bool{}
	for _, root := range []string{"head", "merge-base"} {
		_ = filepath.WalkDir(filepath.Join(dir, root), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || len(paths) > limit {
				return fs.SkipAll
			}
			if entry.Type().IsRegular() {
				if rel, err := filepath.Rel(filepath.Join(dir, root), path); err == nil {
					paths[filepath.ToSlash(rel)] = true
				}
			}
			return nil
		})
	}
	return paths
}

// removeTree removes a directory whose subdirectories may be read-only.
func removeTree(dir string) {
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// WriteFileAtomic writes data to path through a temporary file beside it, so a
// failed run leaves no partial file. The file is readable by its owner only:
// it can hold session text.
func WriteFileAtomic(path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".baum-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// progressHandler prints the loop's milestones as lines for a person. It
// prints counts and fixed words only: never a finding, a model's text or a
// path, so what it writes needs no masking.
type progressHandler struct{ w io.Writer }

func (h progressHandler) Enabled(context.Context, slog.Level) bool { return h.w != nil }

func (h progressHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := map[string]slog.Value{}
	record.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
	switch record.Message {
	case "review document validated":
		pass := attrs["pass"].Int64()
		if attrs["valid"].Bool() {
			_, _ = fmt.Fprintf(h.w, "pass %d: the document is valid\n", pass)
		} else {
			_, _ = fmt.Fprintf(h.w, "pass %d: the validator found problems\n", pass)
		}
	case "review agent failed":
		status := "error"
		if v, ok := attrs["status"]; ok && v.String() != "" {
			status = v.String()
		}
		_, _ = fmt.Fprintf(h.w, "attempt %d failed (%s)\n", attrs["provider_attempt"].Int64(), status)
	}
	return nil
}

func (h progressHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h progressHandler) WithGroup(string) slog.Handler      { return h }

// diagnostics is the file --diagnostics writes: how the run ended and the
// redacted outcome. An early failure has an empty outcome.
type diagnostics struct {
	Result   string        `json:"result"`
	Category Category      `json:"category,omitempty"`
	Error    string        `json:"error,omitempty"`
	Outcome  agent.Outcome `json:"outcome"`
}

// WriteDiagnostics writes the run's end state to path: ok or failed, the
// category and the (already redacted) error text of a failure, and the
// outcome, whose size the loop bounds (at most agent.MaxDiagnostics events
// and a retained document of at most agent.MaxRetainedDocumentBytes).
func WriteDiagnostics(path string, runErr error, outcome agent.Outcome) error {
	d := diagnostics{Result: "ok", Outcome: outcome}
	if runErr != nil {
		d.Result, d.Category, d.Error = "failed", CategoryOf(runErr), runErr.Error()
	}
	encoded, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(encoded, '\n'))
}
