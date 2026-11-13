package generate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/anthropic"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/agent/cursor"
	"github.com/baum-ai/baum/internal/agentdirs"
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
// their own login, in the profiles meant for a person's machine, and the
// Anthropic API under their own key. Tests substitute fakes.
type Backends struct {
	Claude      func(model string) CheckedBackend
	Codex       func(model string) CheckedBackend
	CursorAgent func(model string) CheckedBackend
	Anthropic   func(model string) CheckedBackend
}

// DefaultBackends builds Claude Code with the User profile, Codex with the
// Local profile and the Cursor Agent CLI, which run in the user's environment,
// finding each on PATH, and the Anthropic API with the key from
// ANTHROPIC_API_KEY. The Isolated and Container profiles belong to hosted runs
// and are never built here.
func DefaultBackends() Backends { return DefaultBackendsFor(config.Config{}) }

// DefaultBackendsFor is DefaultBackends with the executables cfg names in
// BackendPaths and the variables of its AgentEnv; Claude Code also gets
// cfg's ClaudeConfigDir as CLAUDE_CONFIG_DIR, and Codex its CodexHome as
// CODEX_HOME. A named path replaces the PATH lookup: when it cannot be run,
// the backend fails naming backend_paths.<name> and PATH is not searched.
func DefaultBackendsFor(cfg config.Config) Backends {
	claudePath, codexPath := cfg.BackendPaths[config.BackendClaude], cfg.BackendPaths[config.BackendCodex]
	cursorPath := cfg.BackendPaths[config.BackendCursorAgent]
	env := cfg.AgentEnviron()
	claudeEnv := env
	if cfg.ClaudeConfigDir != "" {
		// The app's choice for these runs wins over agent_env's.
		claudeEnv = agent.WithUserEnv(env, []string{agentdirs.Claude.Env + "=" + cfg.ClaudeConfigDir})
	}
	codexEnv := env
	if cfg.CodexHome != "" {
		codexEnv = agent.WithUserEnv(env, []string{agentdirs.Codex.Env + "=" + cfg.CodexHome})
	}
	return Backends{
		Claude: func(model string) CheckedBackend {
			return withExplicitPath(&claude.Backend{Binary: claudePath, Model: model, Profile: claude.User, Effort: cfg.Effort, Env: claudeEnv}, config.BackendClaude, claudePath)
		},
		Codex: func(model string) CheckedBackend {
			return withExplicitPath(&codex.Backend{Binary: codexPath, Model: model, Profile: codex.Local, Effort: cfg.Effort, Env: codexEnv}, config.BackendCodex, codexPath)
		},
		CursorAgent: func(model string) CheckedBackend {
			return withExplicitPath(&cursor.Backend{Binary: cursorPath, Model: model, Env: env}, config.BackendCursorAgent, cursorPath)
		},
		Anthropic: func(model string) CheckedBackend {
			return &anthropic.Backend{APIKey: cfg.AnthropicAPIKey, Model: model, Effort: cfg.Effort}
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

// ConcurrentCalls forwards the backend's agent.ConcurrentCaller.
func (e explicitPath) ConcurrentCalls() bool { return ConcurrentCalls(e.CheckedBackend) }

// Fix forwards a fix (agent.Fixer) to a backend that can make one.
func (e explicitPath) Fix(ctx context.Context, request agent.Request) (agent.Result, error) {
	fixer, ok := e.CheckedBackend.(agent.Fixer)
	if !ok {
		return agent.Result{}, fmt.Errorf("%s cannot fix pull requests", e.name)
	}
	return fixer.Fix(ctx, request)
}

// ErrCannotComplete is a backend without single-call completions
// (agent.Completer): cursor-agent.
var ErrCannotComplete = errors.New("the backend cannot make a single structured call")

// Complete forwards a completion (agent.Completer) to a backend that can make
// one, and fails with ErrCannotComplete otherwise.
func (e explicitPath) Complete(ctx context.Context, request agent.CompleteRequest) (agent.Completion, error) {
	completer, ok := e.CheckedBackend.(agent.Completer)
	if !ok {
		return agent.Completion{}, fmt.Errorf("%s: %w", e.name, ErrCannotComplete)
	}
	return completer.Complete(ctx, request)
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
// auto tries, in order, claude, codex and anthropic, each when it passes its
// checks (anthropic's is that ANTHROPIC_API_KEY is set), and fails naming every
// reason when none does. It never picks cursor-agent, which cannot be held to
// read-only as reliably as the others: that backend runs only when named. A
// backend named explicitly fails with its own check's reason.
//
// A model names a model of one backend, so with auto it applies to the first
// choice, claude, only: a backend auto falls back to runs with its own
// default model, and skipped says so. skipped also receives why each earlier
// choice was skipped; nil discards both. anthropic's default model is
// resolved here (anthropic.DefaultModel), so Selected.Model names it and the
// run's cost at API prices can be worked out from it.
func Select(ctx context.Context, requested config.Backend, model string, backends Backends, skipped io.Writer) (Selected, error) {
	return selectFrom(ctx, requested, AutoOrder, model, backends, skipped)
}

// DesktopAutoOrder is the order in which auto tries the backends in the
// desktop app and its daemon: Claude Code, then Codex. auto never spends money
// through the Anthropic API there.
var DesktopAutoOrder = []config.Backend{config.BackendClaude, config.BackendCodex}

// DesktopBackend reports whether the desktop app and its daemon run jobs with
// b: auto, claude and codex. The cursor-agent and anthropic backends stay
// available to the baum command line only.
func DesktopBackend(b config.Backend) bool {
	return b.Desktop()
}

// ErrBackendNotOffered is a backend the desktop app does not run jobs with.
var ErrBackendNotOffered = errors.New("the baum desktop app does not run jobs with this backend")

// NotOfferedError reports a job whose backend, such as one set in config.toml,
// the desktop app does not run. It never switches to another backend.
func NotOfferedError(b config.Backend) error {
	return fmt.Errorf("%w: %q cannot run reviews, fixes, chats or decision capture here; choose Claude Code or Codex in Settings, or set backend = \"auto\", \"claude\" or \"codex\" in the config file", ErrBackendNotOffered, b)
}

// SelectDesktop is Select for the desktop app and its daemon. It refuses a
// known backend other than auto, claude and codex (ErrBackendNotOffered), and
// auto tries DesktopAutoOrder only, so no job reaches cursor-agent or the
// Anthropic API.
func SelectDesktop(ctx context.Context, requested config.Backend, model string, backends Backends, skipped io.Writer) (Selected, error) {
	if requested.Valid() && !DesktopBackend(requested) {
		return Selected{}, &Error{Category: CategoryBackend, Err: NotOfferedError(requested)}
	}
	return selectFrom(ctx, requested, DesktopAutoOrder, model, backends, skipped)
}

func selectFrom(ctx context.Context, requested config.Backend, order []config.Backend, model string, backends Backends, skipped io.Writer) (Selected, error) {
	try := func(name config.Backend, build func(string) CheckedBackend, model string) (Selected, error) {
		if build == nil {
			return Selected{}, fmt.Errorf("the %s backend is not built", name)
		}
		if name == config.BackendAnthropic && model == "" {
			model = anthropic.DefaultModel
		}
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
	explicit := func(name config.Backend, build func(string) CheckedBackend) (Selected, error) {
		selected, err := try(name, build, model)
		if err != nil {
			return fail(err)
		}
		return selected, nil
	}
	switch requested {
	case config.BackendClaude:
		return explicit(requested, backends.Claude)
	case config.BackendCodex:
		return explicit(requested, backends.Codex)
	case config.BackendAnthropic:
		return explicit(requested, backends.Anthropic)
	case config.BackendCursorAgent:
		return explicit(requested, backends.CursorAgent)
	case config.BackendAuto, "":
		var reasons []string
		for i, choice := range order {
			chosen := ""
			if i == 0 {
				chosen = model
			}
			selected, err := try(choice, backends.builder(choice), chosen)
			if err == nil {
				if i > 0 && model != "" && skipped != nil {
					_, _ = fmt.Fprintf(skipped, "the model %q was meant for %s; %s uses its own default model\n", model, order[0], choice)
				}
				return selected, nil
			}
			reasons = append(reasons, fmt.Sprintf("%s: %v", choice, err))
			if skipped != nil && ctx.Err() == nil && i+1 < len(order) {
				_, _ = fmt.Fprintf(skipped, "%s skipped: %v\n", choice, err)
			}
		}
		return fail(fmt.Errorf("no usable model backend: %s", strings.Join(reasons, "; ")))
	default:
		return fail(fmt.Errorf("unknown backend %q", requested))
	}
}

// AutoOrder is the order in which auto tries the backends. The model a run
// names applies to the first only. cursor-agent is left out on purpose: it
// must be chosen explicitly (see SECURITY.md, "The cursor-agent backend").
var AutoOrder = []config.Backend{config.BackendClaude, config.BackendCodex, config.BackendAnthropic}

// builder returns the constructor of a backend, nil when it has none.
func (b Backends) builder(name config.Backend) func(string) CheckedBackend {
	switch name {
	case config.BackendClaude:
		return b.Claude
	case config.BackendCodex:
		return b.Codex
	case config.BackendCursorAgent:
		return b.CursorAgent
	case config.BackendAnthropic:
		return b.Anthropic
	}
	return nil
}
