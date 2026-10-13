package generate

import (
	"context"
	"errors"
	"fmt"

	"github.com/lastpersonlabs/baum/agent"
	"github.com/lastpersonlabs/baum/agent/claude"
	"github.com/lastpersonlabs/baum/agent/codex"
	"github.com/lastpersonlabs/baum/internal/config"
)

// CheckedBackend is a backend that can verify, before any work is done, that
// it is installed, recent enough and able to run.
type CheckedBackend interface {
	agent.Backend
	// Check returns the installed version.
	Check(ctx context.Context) (version string, err error)
}

// Backends builds the local backends. DefaultBackends is the only production
// implementation: the user's installed CLIs under their own login, in the
// profiles meant for a person's machine. Tests substitute fakes.
type Backends struct {
	Claude func(model string) CheckedBackend
	Codex  func(model string) CheckedBackend
}

// DefaultBackends builds Claude Code with the User profile and Codex with the
// Local profile, which run in the user's environment. The Isolated and
// Container profiles belong to hosted runs and are never built here.
func DefaultBackends() Backends {
	return Backends{
		Claude: func(model string) CheckedBackend { return &claude.Backend{Model: model, Profile: claude.User} },
		Codex:  func(model string) CheckedBackend { return &codex.Backend{Model: model, Profile: codex.Local} },
	}
}

// Selected is the backend a run uses.
type Selected struct {
	Backend CheckedBackend
	Version string
}

// Select resolves the requested backend, checking that it can run.
//
// auto picks claude when it is installed and passes its checks, else codex,
// and fails naming both reasons when neither does. claude and codex fail with
// their own check's reason. anthropic and cursor-agent are not implemented in
// this version: auto skips them and naming one fails.
func Select(ctx context.Context, requested config.Backend, model string, backends Backends) (Selected, error) {
	try := func(build func(string) CheckedBackend) (Selected, error) {
		backend := build(model)
		version, err := backend.Check(ctx)
		if err != nil {
			return Selected{}, err
		}
		return Selected{Backend: backend, Version: version}, nil
	}
	fail := func(err error) (Selected, error) {
		if ctx.Err() != nil {
			return Selected{}, &Error{Category: CategoryInterrupted, Err: errors.New("interrupted")}
		}
		return Selected{}, &Error{Category: CategoryBackend, Err: err}
	}
	switch requested {
	case config.BackendClaude:
		selected, err := try(backends.Claude)
		if err != nil {
			return fail(err)
		}
		return selected, nil
	case config.BackendCodex:
		selected, err := try(backends.Codex)
		if err != nil {
			return fail(err)
		}
		return selected, nil
	case config.BackendAnthropic, config.BackendCursorAgent:
		return fail(fmt.Errorf("the %s backend is not available in this version of baum; use --backend claude, codex or auto", requested))
	case config.BackendAuto, "":
		selected, claudeErr := try(backends.Claude)
		if claudeErr == nil {
			return selected, nil
		}
		selected, codexErr := try(backends.Codex)
		if codexErr == nil {
			return selected, nil
		}
		return fail(fmt.Errorf("no usable model backend: claude: %v; codex: %v", claudeErr, codexErr))
	default:
		return fail(fmt.Errorf("unknown backend %q", requested))
	}
}
