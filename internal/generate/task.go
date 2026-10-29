package generate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
)

// TaskOptions are the inputs of one agent task (agent.Task) besides a
// review, such as feature discovery.
type TaskOptions struct {
	Backend agent.Backend
	// Task is the task; its Budgets' WallClock bounds the whole run.
	Task agent.Task
	// PassTimeout bounds each backend call; zero leaves it to the wall clock.
	PassTimeout time.Duration
	Redactor    *bundle.Redactor
	Logger      *slog.Logger
}

// RunTask runs a task through the review harness's loop, as Run runs a
// review: the wall clock and pass bounds apply, what the loop returned is
// masked (agent.RedactTaskOutput) on every path before it is returned, and a
// failure is an *Error of its category. A reply the schema or the task's
// check still rejected after the repairs is a CategoryValidation error, and
// no document is returned with an error. The Outcome is returned on every
// path, for the ledger.
func RunTask(ctx context.Context, o TaskOptions) (agent.Outcome, []byte, error) {
	redactor := o.Redactor
	if redactor == nil {
		var err error
		if redactor, err = bundle.NewRedactor(); err != nil {
			return agent.Outcome{}, nil, err
		}
	}
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if o.Task.Budgets.WallClock > 0 {
		runCtx, cancel = context.WithTimeout(ctx, o.Task.Budgets.WallClock)
	}
	defer cancel()
	backend := o.Backend
	if o.PassTimeout > 0 {
		backend = passBounded{Backend: backend, timeout: o.PassTimeout}
	}
	generator := agent.Generator{Backend: backend, Logger: o.Logger}
	outcome, document, runErr := generator.RunTask(runCtx, o.Task)
	document, redactErr := agent.RedactTaskOutput(context.WithoutCancel(ctx), redactor, document, &outcome)
	fail := func(category Category, err error) error {
		text := err.Error()
		if masked, _, maskErr := redactor.RedactString(context.WithoutCancel(ctx), text); maskErr == nil {
			text = masked
		}
		return &Error{Category: category, Err: errors.New(text)}
	}
	switch {
	case runErr != nil:
		category, cause := classify(ctx, runCtx, runErr)
		return outcome, nil, fail(category, cause)
	case redactErr != nil:
		return outcome, nil, fail(CategoryInternal, fmt.Errorf("masking the output: %w", redactErr))
	case outcome.Reason == agent.ReasonValidationRejected:
		return outcome, nil, rejection(fail, outcome.Findings)
	}
	return outcome, document, nil
}
