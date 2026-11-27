package generate

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles/library"
)

// FillTile fills a library type a person added to a pull request's review,
// for a version generation did not fill it for, in one call
// (agent.Generator.FillTile): it assembles the pull request as a run does
// (o.Assembler, o.Request, its checkout in a run directory removed
// afterwards) and runs the fill with o's backend, budgets and pass timeout.
// Its data is masked and checked against the assembled diff. The result's
// Outcome is what the call spent; an error is a failure before the call.
func FillTile(ctx context.Context, o Options, entry library.Entry) (agent.FillResult, error) {
	redactor := o.Redactor
	if redactor == nil {
		var err error
		if redactor, err = bundle.NewRedactor(); err != nil {
			return agent.FillResult{}, err
		}
	}
	fail := func(category Category, err error) error {
		text := err.Error()
		if masked, _, maskErr := redactor.RedactString(context.WithoutCancel(ctx), text); maskErr == nil {
			text = masked
		}
		return &Error{Category: category, Err: errors.New(text), headChanged: errors.Is(err, ErrHeadChanged)}
	}
	interrupted := func(err error) error {
		if ctx.Err() != nil {
			return &Error{Category: CategoryInterrupted, Err: errors.New("interrupted")}
		}
		return err
	}
	archives, err := os.MkdirTemp("", "baum-fill-*")
	if err != nil {
		return agent.FillResult{}, err
	}
	defer RemoveTree(archives)
	runDir, err := os.MkdirTemp("", agent.RunDirPrefix+"*")
	if err != nil {
		return agent.FillResult{}, err
	}
	defer RemoveTree(runDir)
	assembled, _, err := assembleOne(ctx, o, archives, runDir, fail, interrupted)
	if err != nil {
		return agent.FillResult{}, err
	}
	b := assembled.Bundle
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if o.Budgets.WallClock > 0 {
		runCtx, cancel = context.WithTimeout(ctx, o.Budgets.WallClock)
	}
	defer cancel()
	backend := o.Backend
	if o.PassTimeout > 0 {
		backend = passBounded{Backend: backend, timeout: o.PassTimeout}
	}
	generator := agent.Generator{Backend: backend, Redactor: redactor, Logger: slog.New(&progressHandler{w: o.Progress, repairs: o.OnRepairs, stages: map[string]int{}})}
	return generator.FillTile(runCtx, agent.FillTileRun{Bundle: &b, Directory: runDir, Entry: entry, Prompts: prompt.TilesPrompts{}, Budgets: o.Budgets, Redactor: redactor}), nil
}
