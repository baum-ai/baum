package generate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/config"
)

// CheckedBackend is a backend that can verify, before any work is done, that
// it is installed, recent enough and able to run.
type CheckedBackend interface {
	agent.Backend
	// Check returns the installed version.
	Check(ctx context.Context) (version string, err error)
}

// Backends builds the local backends. DefaultBackends and DefaultBackendsFor
// are the only production implementations: the user's installed CLIs under
// their own login, in the profiles meant for a person's machine. Tests
// substitute fakes.
type Backends struct {
	Claude func(model string) CheckedBackend
	Codex  func(model string) CheckedBackend
}

// DefaultBackends builds Claude Code with the User profile and Codex with the
// Local profile, which run in the user's environment, finding both on PATH.
// The Isolated and Container profiles belong to hosted runs and are never
// built here.
func DefaultBackends() Backends { return DefaultBackendsFor(config.Config{}) }

// DefaultBackendsFor is DefaultBackends with the executables cfg names in
// BackendPaths. A named path replaces the PATH lookup: when it cannot be run,
// the backend fails naming backend_paths.<name> and PATH is not searched.
func DefaultBackendsFor(cfg config.Config) Backends {
	claudePath, codexPath := cfg.BackendPaths[config.BackendClaude], cfg.BackendPaths[config.BackendCodex]
	return Backends{
		Claude: func(model string) CheckedBackend {
			return withExplicitPath(&claude.Backend{Binary: claudePath, Model: model, Profile: claude.User}, config.BackendClaude, claudePath)
		},
		Codex: func(model string) CheckedBackend {
			return withExplicitPath(&codex.Backend{Binary: codexPath, Model: model, Profile: codex.Local}, config.BackendCodex, codexPath)
		},
	}
}

// ExplicitPathError reports a backend_paths entry that names a file which
// cannot be run. Err is why.
type ExplicitPathError struct {
	Backend config.Backend
	Path    string
	Err     error
}

func (e *ExplicitPathError) Error() string {
	return fmt.Sprintf("backend_paths.%s is %q, which cannot be run (%v); fix backend_paths.%s in the config file, or remove it to search PATH", e.Backend, e.Path, e.Err, e.Backend)
}

func (e *ExplicitPathError) Unwrap() error { return e.Err }

// CheckExplicitPath returns an ExplicitPathError when path is not an
// executable file, and nil for an empty path.
func CheckExplicitPath(name config.Backend, path string) error {
	if path == "" {
		return nil
	}
	if _, err := exec.LookPath(path); err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			err = execErr.Err
		}
		return &ExplicitPathError{Backend: name, Path: path, Err: err}
	}
	return nil
}

// explicitPath fails a Check with an ExplicitPathError before the backend's
// own check runs, whose wording suggests installing the tool.
type explicitPath struct {
	CheckedBackend
	name config.Backend
	path string
}

func withExplicitPath(backend CheckedBackend, name config.Backend, path string) CheckedBackend {
	if path == "" {
		return backend
	}
	return explicitPath{CheckedBackend: backend, name: name, path: path}
}

func (e explicitPath) Check(ctx context.Context) (string, error) {
	if err := CheckExplicitPath(e.name, e.path); err != nil {
		return "", err
	}
	return e.CheckedBackend.Check(ctx)
}

// Selected is the backend a run uses.
type Selected struct {
	Backend CheckedBackend
	Version string
	// Model is the model the backend was built with; empty is its default.
	Model string
}

// Select resolves the requested backend, checking that it can run.
//
// auto picks claude when it is installed and passes its checks, else codex,
// and fails naming both reasons when neither does. claude and codex fail with
// their own check's reason. anthropic and cursor-agent are not implemented in
// this version: auto skips them and naming one fails.
//
// A model names a model of one backend, so with auto it applies to the first
// choice, claude, only: when auto falls back to codex, codex runs with its own
// default model, and skipped says so. skipped also receives why claude was
// skipped; nil discards both.
func Select(ctx context.Context, requested config.Backend, model string, backends Backends, skipped io.Writer) (Selected, error) {
	try := func(build func(string) CheckedBackend, model string) (Selected, error) {
		backend := build(model)
		version, err := backend.Check(ctx)
		if err != nil {
			return Selected{}, err
		}
		return Selected{Backend: backend, Version: version, Model: model}, nil
	}
	fail := func(err error) (Selected, error) {
		if ctx.Err() != nil {
			return Selected{}, &Error{Category: CategoryInterrupted, Err: errors.New("interrupted")}
		}
		return Selected{}, &Error{Category: CategoryBackend, Err: err}
	}
	switch requested {
	case config.BackendClaude:
		selected, err := try(backends.Claude, model)
		if err != nil {
			return fail(err)
		}
		return selected, nil
	case config.BackendCodex:
		selected, err := try(backends.Codex, model)
		if err != nil {
			return fail(err)
		}
		return selected, nil
	case config.BackendAnthropic, config.BackendCursorAgent:
		return fail(fmt.Errorf("the %s backend is not available in this version of baum; use --backend claude, codex or auto", requested))
	case config.BackendAuto, "":
		selected, claudeErr := try(backends.Claude, model)
		if claudeErr == nil {
			return selected, nil
		}
		if skipped != nil && ctx.Err() == nil {
			_, _ = fmt.Fprintf(skipped, "claude skipped: %v\n", claudeErr)
			if model != "" {
				_, _ = fmt.Fprintf(skipped, "the model %q was meant for claude; codex uses its own default model\n", model)
			}
		}
		selected, codexErr := try(backends.Codex, "")
		if codexErr == nil {
			return selected, nil
		}
		return fail(fmt.Errorf("no usable model backend: claude: %v; codex: %v", claudeErr, codexErr))
	default:
		return fail(fmt.Errorf("unknown backend %q", requested))
	}
}
