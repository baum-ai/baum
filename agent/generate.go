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
	outcome.SchemaRevision = g.Validator.Revision
	// Reported on every outcome, so a host can tell a loop that ran the
	// identifier pass from one that predates it.
	outcome.IdentifierRedaction = &IdentifierRedaction{}
	prompt, err := g.Prompts.Instructions(ctx, g.Backend.Name())
	if err != nil {
		return outcome, nil, err
	}
	logger := g.Logger
	redactor := g.Redactor
	if redactor == nil {
		if redactor, err = bundle.NewRedactor(); err != nil {
			return outcome, nil, err
		}
	}
	// One redactor for every pass, so its counts cover them all: a secret
	// written in any pass fails the run.
	identifiers, err := review.NewOutputRedactor(g.Validator, redactor, run.Bundle, nil)
	if err != nil {
		return outcome, nil, err
	}
	if err := tilecatalogue.Validate(run.TileKinds); err != nil {
		return outcome, nil, fmt.Errorf("tile kinds: %w", err)
	}
	schema, err := g.Validator.ModelSchema(run.TileKinds)
	if err != nil {
		return outcome, nil, err
	}
	for name, body := range map[string][]byte{"bundle.json": run.EncodedBundle, "schema.json": schema} {
		if err := os.WriteFile(filepath.Join(run.Directory, name), body, 0400); err != nil {
			return outcome, nil, err
		}
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
	text := contextPack
	var assessed []byte
	// Stalls are counted over the run, repairs included.
	stalls := 0
	budgets := run.Budgets
	repairs := max(budgets.MaxRepairAttempts, 1)
	for attempt := 0; ; attempt++ {
		result, callErr := g.call(ctx, budgets, Request{Directory: run.Directory, Prompt: text, Schema: schema, Logger: logger, Endpoint: run.Endpoint, PersistSession: run.PersistSession}, &outcome, &stalls)
		if callErr != nil {
			if attempt > 0 {
				// A repair that could not finish still fails with the call's
				// own reason; the document it was repairing and the findings
				// stay on the outcome for a maintainer to read.
				outcome.RejectedDocument = RetainedDocument(document)
			}
			return outcome, nil, callErr
		}
		// Every call is a fresh session carrying the whole context pack (a
		// repair resends the bundle and the rejected document), so the
		// session of the call that produced the accepted document is
		// self-contained.
		outcome.SessionID = result.SessionID
		document = review.DocumentWithTileKinds(result.Document, run.TileKinds)
		if run.Decorate != nil {
			document = run.Decorate(document)
		}
		if attempt == 0 {
			assessed = document
		} else {
			document = review.PreserveRiskOmissionsForRepair(run.Bundle.Previous, assessed, document)
		}
		outcome.Model = result.Model
		// Prepare respells identifiers into a form no rule detects, so their
		// secrets are masked first, as the model wrote them. Everything built
		// from the document below (the findings, the logs, the repair prompt,
		// a kept rejected document) comes after.
		masked, identifiersErr := identifiers.Identifiers(ctx, document)
		outcome.IdentifierRedaction = &IdentifierRedaction{Masked: identifiers.Masked(), MaskedClosed: identifiers.MaskedClosed()}
		if identifiersErr != nil {
			// The document returned with the error is unmasked.
			if err := ctx.Err(); err != nil {
				return outcome, nil, err
			}
			return outcome, nil, fmt.Errorf("masking identifiers: %w", identifiersErr)
		}
		document, outcome.Findings = g.Validator.Prepare(masked, run.Bundle, run.Directory)
		if logger != nil {
			logger.InfoContext(ctx, "review document validated", "pass", attempt+1, "valid", outcome.Findings == "", "findings_bytes", len(outcome.Findings), "document_bytes", len(document), "findings", boundedText(outcome.Findings, 2048))
		}
		if outcome.Findings == "" {
			break
		}
		// A repair cannot undo a secret written in an identifier: the host
		// rejects the run whatever the later passes return.
		if attempt == repairs || outcome.IdentifierRedaction.MaskedClosed > 0 {
			outcome.Reason = ReasonValidationRejected
			outcome.RejectedDocument = RetainedDocument(document)
			break
		}
		outcome.ValidationRepairs++
		if err := os.WriteFile(filepath.Join(run.Directory, fmt.Sprintf("rejected-%d.json", attempt+1)), document, 0400); err != nil {
			return outcome, nil, err
		}
		text = instructions + repairInstruction + string(run.EncodedBundle) + "\nRejected document:\n" + string(document) + "\nThe validator found:\n" + outcome.Findings + fmt.Sprintf(repairEnding, attempt+1, repairs)
	}
	if err := ctx.Err(); err != nil {
		return outcome, nil, err
	}
	return outcome, document, nil
}

// call runs the backend, retrying a provider failure within the attempt budget
// and a stall within the run's stall retries (stalls counts the run's stalls so
// far), and charging every call and token to the outcome and the run's budgets.
func (g *Generator) call(ctx context.Context, budgets Budgets, request Request, outcome *Outcome, stalls *int) (Result, error) {
	var result Result
	for attempt := 0; attempt < budgets.MaxProviderAttempts; attempt++ {
		request.MaxCalls = budgets.MaxCalls - outcome.Calls
		request.MaxTokens = budgets.MaxTokens - outcome.Tokens
		if request.MaxCalls < 1 {
			return result, errors.New(GenerationBudgetExceededReason("model calls"))
		}
		if request.MaxTokens < 1 {
			return result, errors.New(GenerationBudgetExceededReason("tokens"))
		}
		var err error
		outcome.ProviderAttempts++
		result, err = g.runWatched(ctx, budgets, request)
		if err != nil {
			outcome.ErroredAttempts++
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
		if outcome.Calls > budgets.MaxCalls {
			return result, errors.New(GenerationBudgetExceededReason("model calls"))
		}
		if outcome.Tokens > budgets.MaxTokens {
			return result, errors.New(GenerationBudgetExceededReason("tokens"))
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err == nil {
			return result, nil
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
