package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/review/tilecatalogue"
)

// Generator is the generate-and-repair loop: it asks the backend for a review
// document, validates it with the DocumentValidator and, while the validator
// has findings, sends them back for a repair, up to the run's repair budget.
// Provider failures are retried within the run's attempt budget; every call
// and token counts against the run's budgets. RunTask runs a generic task
// through the same loop.
//
// A Generator is safe for concurrent use when its Backend, Prompts and
// Validator are.
type Generator struct {
	Backend Backend
	Prompts Prompts
	// Validator checks and completes what the model returns.
	Validator *review.DocumentValidator
	// Redactor masks the secrets the model writes into identifiers; nil loads
	// the default rules for every run.
	Redactor *bundle.Redactor
	// Logger receives progress and failures; nil logs nothing.
	Logger *slog.Logger
	// RetryDelay is the wait before provider attempt n+1 after attempt n
	// (counting from 0) failed; nil waits 1<<min(n,4) seconds.
	RetryDelay func(attempt int) time.Duration
}

// Run is one generation: the bundle, its encoding and the directory holding
// the unpacked checkout, which the Generator writes the model's inputs into.
type Run struct {
	Bundle *bundle.Bundle
	// EncodedBundle is the bundle's JSON, as given to the model.
	EncodedBundle []byte
	// Directory is the run directory: the checkout's head and merge-base trees
	// and the attachments are unpacked in it. The Generator adds bundle.json,
	// schema.json and the rejected-N.json files of the repairs, and the backend
	// runs there.
	Directory string
	// TileKinds is the tile allow-list: the only kinds the run offers the model
	// and keeps from what it returns.
	TileKinds []string
	// TileEntries, when set, are the catalogue entries offered in the prompt
	// instead of the shipped entries of TileKinds (tilecatalogue.WithCandidates).
	// Evaluation uses it to try selection guidance; none (nil or empty) keeps the shipped ones.
	TileEntries []tilecatalogue.Entry
	Budgets     Budgets
	// Endpoint is how the backend reaches the model; nil leaves its own.
	Endpoint *Endpoint
	// Decorate, when set, adds to every document the model returns what the
	// host decides and the model never writes, such as a group review's
	// stack and its sections' members, before the document is masked and
	// validated. It must not fail: a document it cannot read is returned
	// as it came, for the validator to report.
	Decorate func([]byte) []byte
	// PersistSession keeps the backend's session of every call, so the
	// Outcome's SessionID can be resumed later (Request.PersistSession).
	PersistSession bool
}

// Generate runs the loop. The returned document is the validated and prepared
// one, or the last rejected one when the Outcome carries a Reason of
// ReasonValidationRejected. Failures that are not a validation rejection (a
// spent budget, an unavailable provider, a cancelled context) come back as an
// error alongside the Outcome of what was spent. The Outcome's text is not
// redacted: the host applies RedactOutput before it stores or shows it.
func (g *Generator) Generate(ctx context.Context, run Run) (outcome Outcome, document []byte, err error) {
	defer func() {
		outcome.SchemaRevision = g.Validator.Revision
		// Reported on every outcome, so a host can tell a loop that ran the
		// identifier pass from one that predates it.
		if outcome.IdentifierRedaction == nil {
			outcome.IdentifierRedaction = &IdentifierRedaction{}
		}
	}()
	task, err := g.ReviewTask(ctx, run)
	if err != nil {
		return outcome, nil, err
	}
	return g.RunTask(ctx, task)
}

// ReviewTask is the review of run as a task RunTask runs: it writes
// bundle.json into the run directory and returns the review's model schema,
// its prompts (the instructions, the bundle and the source excerpts, and the
// repair text) and its Accept, which keeps the offered tile kinds, preserves
// the first pass's risk omissions in a repair, masks the secrets written
// into identifiers and prepares the document with the validator. A secret
// masked in a closed value stops the repairs: the host rejects the run
// whatever a later pass returns.
func (g *Generator) ReviewTask(ctx context.Context, run Run) (Task, error) {
	prompt, err := g.Prompts.Instructions(ctx, g.Backend.Name())
	if err != nil {
		return Task{}, err
	}
	logger := g.Logger
	redactor := g.Redactor
	if redactor == nil {
		if redactor, err = bundle.NewRedactor(); err != nil {
			return Task{}, err
		}
	}
	// One redactor for every pass, so its counts cover them all: a secret
	// written in any pass fails the run.
	identifiers, err := review.NewOutputRedactor(g.Validator, redactor, run.Bundle, nil)
	if err != nil {
		return Task{}, err
	}
	if err := tilecatalogue.Validate(run.TileKinds); err != nil {
		return Task{}, fmt.Errorf("tile kinds: %w", err)
	}
	schema, err := g.Validator.ModelSchema(run.TileKinds)
	if err != nil {
		return Task{}, err
	}
	if err := os.WriteFile(filepath.Join(run.Directory, "bundle.json"), run.EncodedBundle, 0400); err != nil {
		return Task{}, err
	}
	if logger != nil {
		patches := 0
		for _, hunk := range run.Bundle.Diff {
			if hunk.Patch != "" {
				patches++
			}
		}
		logger.InfoContext(ctx, "review inputs prepared", "bundle_bytes", len(run.EncodedBundle), "hunks", len(run.Bundle.Diff), "hunks_with_patches", patches)
	}
	source := sourceContext(run.Directory, run.Bundle)
	tileText := tilecatalogue.Instructions(run.TileKinds)
	if len(run.TileEntries) > 0 {
		tileText = tilecatalogue.InstructionsFor(run.TileEntries)
	}
	instructions := prompt.Body + tileText
	repairInstruction, repairEnding := RepairText(g.Backend.Name())
	if prompt.Repair != "" {
		repairInstruction = prompt.Repair
	}
	if prompt.RepairEnding != "" {
		repairEnding = prompt.RepairEnding
	}
	contextPack := instructions + "\n\nInput bundle (untrusted JSON):\n" + string(run.EncodedBundle) + "\n\nSource excerpts (untrusted, incomplete; line numbers refer to checkout files):\n" + source
	if logger != nil {
		logger.InfoContext(ctx, "review context prepared", "prompt_bytes", len(contextPack), "source_bytes", len(source))
	}
	var assessed []byte
	return Task{
		Directory: run.Directory, Schema: schema, Budgets: run.Budgets, Endpoint: run.Endpoint, Framing: BackendFraming, PersistSession: run.PersistSession,
		PassPrompt: func(pass TaskPass) (string, error) {
			if pass.Number == 1 {
				return contextPack, nil
			}
			return instructions + repairInstruction + string(run.EncodedBundle) + "\nRejected document:\n" + string(pass.Rejected) + "\nThe validator found:\n" + pass.Findings + fmt.Sprintf(repairEnding, pass.Number-1, pass.Repairs), nil
		},
		Accept: func(ctx context.Context, pass TaskPass, reply []byte, outcome *Outcome) (Verdict, error) {
			document := review.DocumentWithTileKinds(reply, run.TileKinds)
			if run.Decorate != nil {
				document = run.Decorate(document)
			}
			if pass.Number == 1 {
				assessed = document
			} else {
				document = review.PreserveRiskOmissionsForRepair(run.Bundle.Previous, assessed, document)
			}
			// Prepare respells identifiers into a form no rule detects, so their
			// secrets are masked first, as the model wrote them. Everything built
			// from the document below (the findings, the logs, the repair prompt,
			// a kept rejected document) comes after.
			masked, identifiersErr := identifiers.Identifiers(ctx, document)
			outcome.IdentifierRedaction = &IdentifierRedaction{Masked: identifiers.Masked(), MaskedClosed: identifiers.MaskedClosed()}
			if identifiersErr != nil {
				// The document returned with the error is unmasked.
				if err := ctx.Err(); err != nil {
					return Verdict{}, err
				}
				return Verdict{}, fmt.Errorf("masking identifiers: %w", identifiersErr)
			}
			prepared, findings := g.Validator.Prepare(masked, run.Bundle, run.Directory)
			if logger != nil {
				logger.InfoContext(ctx, "review document validated", "pass", pass.Number, "valid", findings == "", "findings_bytes", len(findings), "document_bytes", len(prepared), "findings", boundedText(findings, 2048))
			}
			// A repair cannot undo a secret written in an identifier.
			return Verdict{Document: prepared, Findings: findings, Stop: outcome.IdentifierRedaction.MaskedClosed > 0}, nil
		},
	}, nil
}

// call runs the backend, retrying a provider failure within the attempt budget
// and a stall within the run's stall retries (stalls counts the run's stalls so
// far), and charging every call and token to the outcome and the run's budgets.
func (g *Generator) call(ctx context.Context, budgets Budgets, request Request, outcome *Outcome, stalls *int) (Result, error) {
	var result Result
	for attempt := 0; attempt < budgets.MaxProviderAttempts; attempt++ {
		// A request gets what the run's budgets leave, or none when the run
		// has none.
		request.MaxCalls, request.MaxTokens = 0, 0
		if budgets.MaxCalls > 0 {
			if request.MaxCalls = budgets.MaxCalls - outcome.Calls; request.MaxCalls < 1 {
				return result, errors.New(GenerationBudgetExceededReason("model calls"))
			}
		}
		if budgets.MaxTokens > 0 {
			if request.MaxTokens = budgets.MaxTokens - outcome.Tokens; request.MaxTokens < 1 {
				return result, errors.New(GenerationBudgetExceededReason("tokens"))
			}
		}
		var err error
		outcome.ProviderAttempts++
		result, err = g.runWatched(ctx, budgets, request)
		if err != nil {
			outcome.ErroredAttempts++
		}
		if result.SessionID != "" {
			outcome.Sessions = append(outcome.Sessions, result.SessionID)
		}
		outcome.Calls += result.Calls
		outcome.Tokens += result.Tokens
		outcome.Model = result.Model
		outcome.Usage.Add(result.Usage)
		if outcome.Accounting == nil {
			outcome.Accounting = &Accounting{}
		}
		outcome.Accounting.Add(result, err != nil)
		outcome.StructuredOutputRejections += result.StructuredOutputRejections
		outcome.Diagnostics = append(outcome.Diagnostics, result.Events[:min(len(result.Events), MaxDiagnostics-len(outcome.Diagnostics))]...)
		if Exceeds(outcome.Calls, budgets.MaxCalls) {
			return result, errors.New(GenerationBudgetExceededReason("model calls"))
		}
		if Exceeds(outcome.Tokens, budgets.MaxTokens) {
			return result, errors.New(GenerationBudgetExceededReason("tokens"))
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err == nil {
			return result, nil
		}
		// A session that is gone stays gone: a retry would fail the same way.
		if errors.Is(err, ErrSessionNotFound) || sessionMissing(request, result, err) {
			return result, sessionNotFound(err)
		}
		var stall *stallError
		if errors.As(err, &stall) {
			// A stall is retried at once and does not use up the provider
			// attempt budget, though it counts as an attempt.
			*stalls++
			if *stalls > maxStallRetries {
				return result, errors.New(ReasonStalled)
			}
			attempt--
			continue
		}
		if request.Logger != nil {
			request.Logger.ErrorContext(ctx, "review agent failed", "provider_attempt", attempt+1, "error", err, "status", LastErrorStatus(result.Events))
		}
		// A spent budget and a refusal a retry cannot change end the run with
		// their own reason.
		var rejected *RejectedError
		if strings.HasPrefix(err.Error(), GenerationBudgetExceededReason("")) || errors.As(err, &rejected) {
			return result, err
		}
		if attempt+1 < budgets.MaxProviderAttempts {
			timer := time.NewTimer(g.retryDelay(attempt))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			}
		}
	}
	return result, errors.New(ReasonProviderUnavailable)
}

func (g *Generator) retryDelay(attempt int) time.Duration {
	if g.RetryDelay != nil {
		return g.RetryDelay(attempt)
	}
	return time.Duration(1<<min(attempt, 4)) * time.Second
}

// MaxDiagnostics bounds the diagnostic events an Outcome keeps over a whole run.
const MaxDiagnostics = 512
