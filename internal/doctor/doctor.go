// Package doctor reports why baum can or cannot find its tools, backend and
// GitHub token, with a fix for each problem.
//
// It runs "<tool> --version" for git and gh, the same checks "baum internal generate"
// runs for claude, codex (including codex's read-only sandbox preflight) and
// cursor-agent (its --help and --version), and for anthropic (that
// ANTHROPIC_API_KEY is set; the key is not tried against the API), "gh auth
// token" when no token is configured,
// and at most one network request, for the signed price list. The tools run with the
// environment their backends give them, never the whole of ours. It never
// prints a token or an API key, only where one was found.
package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/proc"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// Status is the outcome of one check. Info reports a fact that is neither
// good nor bad.
type Status string

const (
	StatusOK      Status = "ok"
	StatusWarning Status = "warning"
	StatusProblem Status = "problem"
	StatusInfo    Status = "info"
)

// Check is one finding. Fix is set on every warning and problem.
type Check struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  Status `json:"status"`
	Detail  string `json:"detail"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// Report is the ordered checks and the worst status among them: problem,
// else warning, else ok. Info checks do not change it.
type Report struct {
	Status Status  `json:"status"`
	Checks []Check `json:"checks"`
}

// Problems and Warnings count the checks with that status.
func (r Report) Problems() int { return r.count(StatusProblem) }
func (r Report) Warnings() int { return r.count(StatusWarning) }

func (r Report) count(status Status) int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == status {
			n++
		}
	}
	return n
}

// Options configure Run. The zero value inspects the real environment; tests
// replace the pieces that touch it.
type Options struct {
	// FetchPrices fetches the signed price list first. It is the report's one
	// network request.
	FetchPrices bool

	Getenv   func(string) string
	LookPath func(string) (string, error)
	// Run runs path with args in exactly env and returns its combined output,
	// under ctx. The report decides env per tool and never passes our own.
	Run func(ctx context.Context, env []string, path string, args ...string) (string, error)
	// Backends builds the backends to check; nil builds the real ones from
	// the configuration, as "baum internal generate" does.
	Backends func(config.Config) generate.Backends
	// GitHubToken resolves the token for host; nil uses the configuration's
	// own resolver.
	GitHubToken func(ctx context.Context, cfg config.Config, host string) (token, source string, err error)
	// RefreshPrices fetches the signed list; nil uses pricing.Refresh without
	// storing it, since a diagnostic run must not change the daemon's state.
	RefreshPrices func(ctx context.Context) error
	// PriceSource describes the active list; nil uses pricing.CurrentSource.
	PriceSource func() pricing.Source
	// Registry lists the registered clones of the registry file at path,
	// which exists. The default opens it read-only.
	Registry func(ctx context.Context, path string) ([]repos.Clone, error)
	// Daemon describes the per-user baum daemon; nil omits that check. The
	// caller supplies it because the daemon itself serves this report.
	Daemon func(ctx context.Context) Daemon
	// Executable is this baum's executable, compared with the baum on PATH;
	// nil is os.Executable.
	Executable func() (string, error)
}

// Daemon is what the report says about the per-user daemon. It never holds
// the daemon's credential.
type Daemon struct {
	Running  bool
	Version  string
	Protocol int
	PID      int
	Uptime   time.Duration
	// State is the daemon's state directory (logs, credential) and Socket its
	// control socket.
	State, Socket string
	// Older is set when this baum would replace the daemon; Incompatible when
	// it speaks a newer protocol than this baum.
	Older, Incompatible bool
	// Err is why the daemon could not be asked, when it holds its lock but
	// does not answer.
	Err error
}

// Timeouts bound every subprocess and network request the report makes.
const (
	versionTimeout = 10 * time.Second
	backendTimeout = 90 * time.Second // codex: version, then two sandbox runs
	tokenTimeout   = 30 * time.Second // the keyring may wait for the user to unlock it
	priceTimeout   = 15 * time.Second
	registryTime   = 30 * time.Second
)

var versionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// olderPattern reads the installed version from a backend's "X is older than Y"
// failure.
var olderPattern = regexp.MustCompile(`\b(\d+\.\d+\.\d+) is older than`)

// Run builds the report. It never fails: a problem it meets becomes a check.
func Run(ctx context.Context, opts Options) Report {
	r := &runner{ctx: ctx, opts: opts}
	r.defaults()
	r.run()
	report := Report{Status: StatusOK, Checks: r.checks}
	switch {
	case report.Problems() > 0:
		report.Status = StatusProblem
	case report.Warnings() > 0:
		report.Status = StatusWarning
	}
	return report
}

type runner struct {
	ctx    context.Context
	opts   Options
	checks []Check
	cfg    config.Config
	cfgErr error
	// cfgFileErr is set when the config file itself could not be read or
	// parsed, so its settings, github_token included, were not used.
	cfgFileErr error
	cfgPath    string
	// cfgPathKnown is false when the config directory cannot be found, so no
	// file was looked at.
	cfgPathKnown bool
}

func (r *runner) defaults() {
	o := &r.opts
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Run == nil {
		o.Run = runCommand
	}
	if o.Backends == nil {
		o.Backends = generate.DefaultBackendsFor
	}
	if o.GitHubToken == nil {
		o.GitHubToken = func(ctx context.Context, cfg config.Config, host string) (string, string, error) {
			return cfg.ResolveGitHubTokenSource(ctx, host)
		}
	}
	if o.RefreshPrices == nil {
		o.RefreshPrices = func(ctx context.Context) error { return pricing.Refresh(ctx, "") }
	}
	if o.PriceSource == nil {
		o.PriceSource = pricing.CurrentSource
	}
	if o.Registry == nil {
		o.Registry = listClones
	}
	if o.Executable == nil {
		o.Executable = os.Executable
	}
}

func runCommand(ctx context.Context, env []string, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append([]string{}, env...)
	// A timeout kills the whole process group, so a tool's own children do not
	// outlive it, and WaitDelay stops one that holds the pipe from stalling.
	proc.Configure(cmd)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if len(out) > 4096 {
		out = out[:4096]
	}
	return string(out), err
}

func listClones(ctx context.Context, path string) ([]repos.Clone, error) {
	registry, err := repos.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = registry.Close() }()
	return registry.List(ctx)
}

func (r *runner) add(c Check) { r.checks = append(r.checks, c) }

func (r *runner) run() {
	var pathErr error
	r.cfgPath, pathErr = config.Path()
	r.cfgPathKnown = pathErr == nil
	if !r.cfgPathKnown {
		// Fix texts name a file; never read one from the working directory.
		r.cfgPath = "config.toml"
	}
	cfg, cfgErr := config.Load(config.Overrides{})
	if cfgErr != nil {
		// The commands would stop here. Report with what can still be read: the
		// file when it parses on its own (the failure was a BAUM_BACKEND value or
		// the file's backend name), else the defaults; then BAUM_MODEL and the
		// token variables as the commands would have read them, which take
		// precedence over the file's.
		fileErr := pathErr
		if r.cfgPathKnown {
			cfg, _, fileErr = config.ReadFile(r.cfgPath)
		}
		if fileErr != nil {
			cfg = config.Default()
			r.cfgFileErr = fileErr
		} else if b, err := config.ParseBackend(string(cfg.Backend)); err == nil {
			// Load accepts any case, so only a BAUM_BACKEND failure may remain.
			cfg.Backend = b
		}
		if v := r.opts.Getenv(config.EnvModel); v != "" {
			cfg.Model = v
		}
		for _, name := range []string{config.EnvGHToken, config.EnvGitHubToken} {
			if v := r.opts.Getenv(name); v != "" {
				cfg.GitHubToken, cfg.GitHubTokenSource = v, name
			}
		}
		cfg.AnthropicAPIKey = r.opts.Getenv(config.EnvAnthropicAPIKey)
	}
	r.cfg, r.cfgErr = cfg, cfgErr

	r.git()
	r.gh()
	checked := r.backends()
	r.selection(checked)
	clones := r.locations(cfgErr)
	r.tokens(clones)
	r.anthropicKey()
	r.prices()
	r.command()
	r.environment()
}

// tool resolves the executable name: explicit, a configured absolute path,
// replaces the PATH lookup. source says where it came from, also on failure.
func (r *runner) tool(name, explicit string) (path, source string, err error) {
	if explicit != "" {
		if err := generate.CheckExplicitPath(config.Backend(name), explicit); err != nil {
			return "", "backend_paths." + name + " in " + r.cfgPath, err
		}
		return explicit, "backend_paths." + name + " in " + r.cfgPath, nil
	}
	path, err = r.opts.LookPath(name)
	if err != nil {
		return "", "PATH", err
	}
	return path, "PATH", nil
}

// version runs path --version in env and returns the first version in its
// output, and the output.
func (r *runner) version(env []string, path string) (string, string, error) {
	ctx, cancel := context.WithTimeout(r.ctx, versionTimeout)
	defer cancel()
	out, err := r.opts.Run(ctx, env, path, "--version")
	if err != nil {
		return "", out, err
	}
	return versionPattern.FindString(out), out, nil
}

func (r *runner) git() {
	c := Check{ID: "git", Label: "git"}
	path, source, err := r.tool("git", "")
	c.Source = source
	if err != nil {
		c.Status, c.Detail = StatusProblem, "git was not found on PATH"
		c.Fix = "install git " + gitlocal.MinGitVersion + " or newer and make sure it is on PATH"
		r.add(c)
		return
	}
	c.Path = path
	// DEVELOPER_DIR picks the toolchain behind macOS's /usr/bin/git shim, which
	// gitlocal runs with it, so the version reported is the one baum uses.
	version, out, err := r.version(agent.LocalEnvironment(os.Environ(), "DEVELOPER_DIR"), path)
	switch {
	case err != nil:
		c.Status, c.Detail = StatusProblem, fmt.Sprintf("running %s --version failed: %v", path, err)
		c.Fix = fmt.Sprintf("run `%s --version` yourself to see why it fails, or reinstall git %s or newer", path, gitlocal.MinGitVersion)
	default:
		c.Version = version
		if err := gitlocal.SupportedGitVersion(out); err != nil {
			c.Status, c.Detail = StatusProblem, err.Error()
			c.Fix = "upgrade git to " + gitlocal.MinGitVersion + " or newer"
		} else {
			c.Status, c.Detail = StatusOK, "git "+orUnknown(version)+" is recent enough (needs "+gitlocal.MinGitVersion+" or newer)"
		}
	}
	r.add(c)
}

func (r *runner) gh() {
	c := Check{ID: "gh", Label: "gh (GitHub CLI)"}
	var path string
	var err error
	if explicit := r.opts.Getenv("GH_PATH"); explicit != "" {
		c.Source = "GH_PATH"
		if path, err = r.opts.LookPath(explicit); err != nil {
			c.Status, c.Detail = StatusWarning, fmt.Sprintf("GH_PATH is %q, which cannot be run: %v", explicit, unwrapExec(err))
			c.Fix = "point GH_PATH at the gh executable, or unset it to search PATH; gh is only needed to read a token from `gh auth login`"
			r.add(c)
			return
		}
	} else {
		c.Source = "PATH"
		if path, err = r.opts.LookPath("gh"); err != nil {
			c.Status, c.Detail = StatusWarning, "gh was not found on PATH; it is only needed when the GitHub token comes from `gh auth login`"
			c.Fix = "install the GitHub CLI and run `gh auth login`, or set GITHUB_TOKEN, or set GH_PATH to the gh executable"
			r.add(c)
			return
		}
	}
	c.Path = path
	version, _, err := r.version(agent.LocalEnvironment(os.Environ()), path)
	if err != nil {
		c.Status, c.Detail = StatusWarning, fmt.Sprintf("running %s --version failed: %v", path, err)
		c.Fix = fmt.Sprintf("run `%s --version` yourself to see why it fails, or reinstall gh", path)
	} else {
		c.Status, c.Detail, c.Version = StatusOK, "gh found; used only to read a token from `gh auth login`", version
	}
	r.add(c)
}

// backendTools names the tool of each backend the checks run.
var backendTools = map[config.Backend]string{
	config.BackendClaude:      "Claude Code",
	config.BackendCodex:       "Codex",
	config.BackendCursorAgent: "the Cursor Agent CLI",
}

// checkResult is what a backend's Check returned.
type checkResult struct {
	version string
	err     error
}

// checkedBackends holds the claude, codex, cursor-agent and anthropic
// results, which selection replays.
type checkedBackends struct{ claude, codex, cursorAgent, anthropic checkResult }

// backends checks claude, codex and cursor-agent with the checks "baum
// generate" runs, once each, and anthropic's, which only looks for the key and
// is reported by the key's own check; selection replays the results instead of
// running them again.
func (r *runner) backends() checkedBackends {
	built := r.opts.Backends(r.cfg)
	var result checkedBackends
	if built.Claude != nil {
		result.claude = r.backend(config.BackendClaude, built.Claude(r.cfg.Model))
	} else {
		result.claude.err = errors.New("no claude backend")
	}
	if built.Codex != nil {
		result.codex = r.backend(config.BackendCodex, built.Codex(""))
	} else {
		result.codex.err = errors.New("no codex backend")
	}
	if built.CursorAgent != nil {
		result.cursorAgent = r.backend(config.BackendCursorAgent, built.CursorAgent(""))
	} else {
		result.cursorAgent.err = errors.New("no cursor-agent backend")
	}
	if built.Anthropic != nil {
		result.anthropic.version, result.anthropic.err = built.Anthropic("").Check(r.ctx)
	} else {
		result.anthropic.err = errors.New("no anthropic backend")
	}
	return result
}

func (r *runner) backend(name config.Backend, backend generate.CheckedBackend) checkResult {
	tool := backendTools[name]
	c := Check{ID: string(name), Label: string(name)}
	explicit := r.cfg.BackendPaths[name]
	path, source, lookErr := r.tool(string(name), explicit)
	c.Source, c.Path = source, path

	ctx, cancel := context.WithTimeout(r.ctx, backendTimeout)
	version, err := backend.Check(ctx)
	cancel()
	// The cursor-agent backend falls back to an executable called agent once
	// its help shows it is the Cursor Agent.
	if name == config.BackendCursorAgent && explicit == "" && lookErr != nil && err == nil {
		if agentPath, agentErr := r.opts.LookPath("agent"); agentErr == nil {
			c.Path, c.Source, lookErr = agentPath, "PATH (as agent)", nil
		}
	}
	// A failed check is a problem only for the backend the user asked for, or
	// for a path they wrote that does not work; auto needs just one.
	severity := StatusWarning
	if r.cfg.Backend == name {
		severity = StatusProblem
	}
	var explicitErr *generate.ExplicitPathError
	switch {
	case err == nil:
		c.Status, c.Version = StatusOK, version
		c.Detail = tool + " is installed and passes baum's checks"
	case errors.As(err, &explicitErr) || (explicit != "" && lookErr != nil):
		c.Status, c.Detail, c.Path = StatusProblem, err.Error(), ""
		c.Fix = fmt.Sprintf("fix backend_paths.%s in %s, or remove it to search PATH", name, r.cfgPath)
	case lookErr != nil && name == config.BackendCursorAgent && severity != StatusProblem:
		// Few machines have it, and auto does not need it.
		c.Status, c.Detail = StatusInfo, "cursor-agent was not found on PATH; it is needed only for the cursor-agent backend (install the Cursor Agent CLI and sign in with cursor-agent login to use it)"
	case lookErr != nil:
		c.Status, c.Detail = severity, tool+" was not found on PATH"
		c.Fix = fmt.Sprintf("install %s and sign in, or set backend_paths.%s to its absolute path in %s (apps started without a shell's PATH need this)", tool, name, r.cfgPath)
	default:
		c.Status, c.Detail = severity, err.Error()
		c.Fix = checkFix(name, tool, path, err)
		// The tool is not run again: its version is in the message when the check
		// got far enough to read it.
		if m := olderPattern.FindStringSubmatch(err.Error()); m != nil {
			c.Version = m[1]
		}
	}
	r.add(c)
	return checkResult{version, err}
}

func checkFix(name config.Backend, tool, path string, err error) string {
	if name == config.BackendCursorAgent {
		switch msg := err.Error(); {
		case strings.Contains(msg, "does not list the options"):
			// The detail names the update command of the executable it resolved.
			return "upgrade the Cursor Agent CLI with the update command named above"
		case strings.Contains(msg, "does not say it is"):
			return "point backend_paths.cursor-agent at the Cursor Agent CLI, or remove it to search PATH"
		}
		return fmt.Sprintf("run `%s --help` and `%s --version` yourself to see why they fail, or reinstall %s", path, path, tool)
	}
	switch msg := err.Error(); {
	case strings.Contains(msg, "older than"):
		return "upgrade " + tool + " to the version named above or newer"
	case strings.Contains(msg, "sandbox"):
		return "baum runs codex only inside its read-only sandbox; run `" + path + " sandbox -P :read-only -- cat /dev/null` to see why it fails (on Linux it needs user namespaces), or use claude"
	}
	return fmt.Sprintf("run `%s --version` yourself to see why it fails, or reinstall %s", path, tool)
}

// replay is a backend whose Check returns the result already measured.
type replay struct {
	generate.CheckedBackend
	version string
	err     error
}

func (p replay) Check(context.Context) (string, error) { return p.version, p.err }

func (r *runner) selection(checked checkedBackends) {
	built := r.opts.Backends(r.cfg)
	replayed := generate.Backends{}
	if built.Claude != nil {
		replayed.Claude = func(model string) generate.CheckedBackend {
			return replay{CheckedBackend: built.Claude(model), version: checked.claude.version, err: checked.claude.err}
		}
	}
	if built.Codex != nil {
		replayed.Codex = func(model string) generate.CheckedBackend {
			return replay{CheckedBackend: built.Codex(model), version: checked.codex.version, err: checked.codex.err}
		}
	}
	if built.CursorAgent != nil {
		replayed.CursorAgent = func(model string) generate.CheckedBackend {
			return replay{CheckedBackend: built.CursorAgent(model), version: checked.cursorAgent.version, err: checked.cursorAgent.err}
		}
	}
	if built.Anthropic != nil {
		replayed.Anthropic = func(model string) generate.CheckedBackend {
			return replay{CheckedBackend: built.Anthropic(model), version: checked.anthropic.version, err: checked.anthropic.err}
		}
	}

	var skipped bytes.Buffer
	selected, err := generate.Select(r.ctx, config.BackendAuto, r.cfg.Model, replayed, &skipped)
	c := Check{ID: "backend-auto", Label: "backend: auto"}
	reasons := strings.TrimSpace(skipped.String())
	if err != nil {
		c.Status, c.Detail = StatusProblem, "auto finds no usable backend: "+err.Error()
		c.Fix = fmt.Sprintf("install Claude Code or Codex, or set backend_paths.claude or backend_paths.codex to its absolute path in %s, or set ANTHROPIC_API_KEY to use the Anthropic API; see the claude and codex checks above. auto never picks cursor-agent: set backend = \"cursor-agent\" to use it", r.cfgPath)
	} else {
		c.Status, c.Version = StatusOK, selected.Version
		c.Detail = fmt.Sprintf("auto picks %s, the first of claude, codex and anthropic (with ANTHROPIC_API_KEY set) that passes its checks; it never picks cursor-agent, which cannot be held to read-only as reliably and runs only when configured", selected.Backend.Name())
		if reasons != "" {
			c.Detail += "\n" + reasons
		}
	}
	r.add(c)

	if r.cfg.Backend == config.BackendAuto {
		return
	}
	if !r.cfg.Backend.Valid() {
		// Only a file can hold this: the environment and flags are checked on load.
		name := string(r.cfg.Backend)
		if len(name) > 64 {
			name = name[:64] + "..."
		}
		r.add(Check{ID: "backend-configured", Label: "backend: " + name, Status: StatusProblem,
			Detail: fmt.Sprintf("the configured backend %q is not a known backend", name),
			Fix:    fmt.Sprintf("unknown backend %s; set backend = \"auto\", \"claude\", \"codex\", \"cursor-agent\" or \"anthropic\" in %s", name, r.cfgPath)})
		return
	}
	configured := Check{ID: "backend-configured", Label: "backend: " + string(r.cfg.Backend)}
	selected, err = generate.Select(r.ctx, r.cfg.Backend, r.cfg.Model, replayed, nil)
	if err != nil {
		configured.Status, configured.Detail = StatusProblem, fmt.Sprintf("the configured backend %s cannot run: %v", r.cfg.Backend, err)
		switch r.cfg.Backend {
		case config.BackendAnthropic:
			configured.Fix = fmt.Sprintf("export ANTHROPIC_API_KEY with your Anthropic API key, or set backend = \"auto\", \"claude\", \"codex\" or \"cursor-agent\" in %s (BAUM_BACKEND and --backend override it)", r.cfgPath)
		default:
			configured.Fix = fmt.Sprintf("fix the %s check above, or set backend = \"auto\", \"claude\", \"codex\", \"cursor-agent\" or \"anthropic\" in %s (BAUM_BACKEND and --backend override it)", r.cfg.Backend, r.cfgPath)
		}
	} else {
		configured.Status, configured.Version = StatusOK, selected.Version
		configured.Detail = fmt.Sprintf("the configured backend %s is usable (auto is not used)", selected.Backend.Name())
	}
	r.add(configured)
}

// locations reports where baum keeps its files and returns the registered
// clones. It never creates a file or a directory.
func (r *runner) locations(cfgErr error) []repos.Clone {
	cfg := Check{ID: "config", Label: "config file", Path: r.cfgPath, Status: StatusInfo}
	if !r.cfgPathKnown {
		cfg.Path = ""
	}
	switch _, statErr := os.Stat(r.cfgPath); {
	case cfgErr != nil && !r.cfgPathKnown:
		cfg.Status, cfg.Detail = StatusProblem, "the configuration does not load: "+cfgErr.Error()
		cfg.Fix = "set HOME or XDG_CONFIG_HOME so baum can find its config directory; the other checks use default settings, and no config file is read"
	case cfgErr != nil && r.cfgFileErr != nil:
		cfg.Status, cfg.Detail = StatusProblem, "the configuration does not load: "+cfgErr.Error()
		cfg.Fix = fmt.Sprintf("correct the setting named above in %s (or the BAUM_BACKEND environment variable), or remove it; the other checks use default settings", r.cfgPath)
	case cfgErr != nil:
		cfg.Status, cfg.Detail = StatusProblem, "the configuration does not load: "+cfgErr.Error()
		cfg.Fix = fmt.Sprintf("correct the BAUM_BACKEND environment variable, or the backend value in %s; the file itself parses, so the other checks use its settings", r.cfgPath)
	case statErr == nil:
		cfg.Detail = "read from this file; flags and BAUM_* variables override it"
	default:
		cfg.Detail = "does not exist; baum uses its defaults"
	}
	r.add(cfg)

	var clones []repos.Clone
	registryPath, err := repos.Path()
	switch {
	case err != nil:
		r.add(Check{ID: "registry", Label: "repository registry", Status: StatusWarning, Detail: err.Error(), Fix: "set HOME or XDG_CONFIG_HOME so baum can find its config directory"})
	default:
		c := Check{ID: "registry", Label: "repository registry", Path: registryPath, Status: StatusInfo}
		if _, statErr := os.Stat(registryPath); statErr != nil {
			c.Detail = "not created yet; no clones are registered (baum internal repos add <path>)"
		} else {
			ctx, cancel := context.WithTimeout(r.ctx, registryTime)
			var listErr error
			clones, listErr = r.opts.Registry(ctx, registryPath)
			cancel()
			if listErr != nil {
				c.Status, c.Detail = StatusWarning, "cannot read the registry: "+listErr.Error()
				c.Fix = "check that " + registryPath + " is a baum registry that you can read and that no other process holds locked; `baum internal repos list` shows the same error"
			} else {
				c.Detail = fmt.Sprintf("%d registered %s", len(clones), plural(len(clones), "clone", "clones"))
			}
		}
		r.add(c)
	}

	r.location("jobs", "job queue", jobs.DefaultPath, "created when the baum daemon runs its first job")
	r.location("discovery", "discovery cache", discovery.DefaultDirectory, "created when the baum daemon first looks for pull requests")
	if r.opts.Daemon != nil {
		r.daemon()
	}

	for _, clone := range clones {
		label := "store: " + clone.Repository
		if !clone.Available {
			r.add(Check{ID: "store-" + clone.ID, Label: label, Path: clone.Path, Status: StatusWarning,
				Detail: "the clone is not available at its registered path, so its store is not read",
				Fix:    fmt.Sprintf("restore the clone at %s, or run `baum internal repos remove %s`", clone.Path, clone.ID)})
			continue
		}
		s, err := store.Open(clone.CommonDir, "", false)
		if err != nil {
			r.add(Check{ID: "store-" + clone.ID, Label: label, Path: clone.Path, Status: StatusWarning,
				Detail: "cannot locate the version store: " + err.Error(),
				Fix:    "check the permissions of " + clone.CommonDir + " and of your XDG_DATA_HOME"})
			continue
		}
		c := Check{ID: "store-" + clone.ID, Label: label, Path: s.Root(), Status: StatusInfo, Detail: "no versions stored yet"}
		if _, err := os.Stat(filepath.Join(s.Root(), "versions.sqlite")); err == nil {
			c.Detail = "version store for the clone at " + clone.Path
		}
		r.add(c)
	}
	return clones
}

// daemon reports where the daemon lives and whether it runs. It only asks
// the daemon over its socket; it starts nothing.
func (r *runner) daemon() {
	ctx, cancel := context.WithTimeout(r.ctx, versionTimeout)
	defer cancel()
	d := r.opts.Daemon(ctx)
	c := Check{ID: "daemon", Label: "baum daemon", Status: StatusInfo, Path: d.State, Version: d.Version}
	switch {
	case d.Err != nil:
		c.Status, c.Detail = StatusWarning, d.Err.Error()
		c.Fix = "stop it with `baum daemon stop`, or end the process it names (after `baum daemon stop`, agent hooks do not start it again until `baum daemon ensure` or the app runs); its logs are in " + d.State
	case !d.Running:
		c.Detail = "not running; the desktop app (`baum daemon ensure`) and agent hooks start it, except hooks after `baum daemon stop`"
	case d.Incompatible:
		c.Status = StatusWarning
		c.Detail = fmt.Sprintf("running as process %d for %s, but it speaks control protocol %d, newer than this baum", d.PID, d.Uptime, d.Protocol)
		c.Fix = "upgrade baum, or stop the daemon with `baum daemon stop`; agent hooks do not start it again until `baum daemon ensure` (or the app) runs"
	case d.Older:
		c.Status = StatusWarning
		c.Detail = fmt.Sprintf("running as process %d for %s, older than this baum", d.PID, d.Uptime)
		c.Fix = "run `baum daemon ensure` to replace it with this version"
	default:
		c.Detail = fmt.Sprintf("running as process %d for %s, control protocol %d; socket %s", d.PID, d.Uptime, d.Protocol, d.Socket)
	}
	r.add(c)
}

func (r *runner) location(id, label string, resolve func() (string, error), created string) {
	path, err := resolve()
	if err != nil {
		r.add(Check{ID: id, Label: label, Status: StatusWarning, Detail: err.Error(), Fix: "set HOME (or the XDG directories) so baum can find its state directories"})
		return
	}
	c := Check{ID: id, Label: label, Path: path, Status: StatusInfo, Detail: "exists"}
	if _, err := os.Stat(path); err != nil {
		c.Detail = "does not exist yet; " + created
	}
	r.add(c)
}

// tokens reports where the GitHub token comes from for github.com and for each
// other host a registered clone belongs to. Only the source is reported.
func (r *runner) tokens(clones []repos.Clone) {
	hosts := []string{config.GitHubHost}
	for _, clone := range clones {
		host := config.GitHubHost
		if parts := strings.Split(clone.Repository, "/"); len(parts) == 3 {
			host = strings.ToLower(parts[0])
		}
		if !contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	for _, host := range hosts {
		c := Check{ID: "github-token-" + host, Label: "GitHub token: " + host}
		if !github.IsGitHubHost(host) {
			c.Status = StatusProblem
			c.Detail = fmt.Sprintf("baum will not read %s: it is not github.com, a *.ghe.com tenant, a host in gh's hosts.yml or the host GH_HOST names, so a token is never looked up or sent for it", host)
			c.Fix = fmt.Sprintf("set GH_HOST=%s or run `gh auth login --hostname %s`", host, host)
			r.add(c)
			continue
		}
		ctx, cancel := context.WithTimeout(r.ctx, tokenTimeout)
		_, source, err := r.opts.GitHubToken(ctx, r.cfg, host)
		cancel()
		unread := ""
		if r.cfgFileErr != nil && host == config.GitHubHost {
			unread = ". The config file did not load, so its github_token was not read"
		}
		switch {
		case err == nil:
			c.Status, c.Source = StatusOK, source
			c.Detail = "a token is available for " + host + unread
		case errors.Is(err, config.ErrNoGitHubToken):
			_, fix, _ := strings.Cut(err.Error(), ": ")
			c.Status, c.Fix = StatusWarning, fix
			c.Detail = "no token was found for " + host + unread
			if unread != "" {
				c.Fix = "fix the config file as the config check says (its github_token is read once it loads), or set GITHUB_TOKEN, or run `gh auth login`"
			}
			if host == config.GitHubHost {
				c.Detail += "; public repositories work without one, within GitHub's low unauthenticated rate limit"
			} else {
				c.Status = StatusProblem
				c.Detail += ", and a registered clone belongs to it"
			}
		case errors.Is(err, context.DeadlineExceeded):
			c.Status, c.Detail = StatusProblem, fmt.Sprintf("asking gh for the token of %s timed out after %s", host, tokenTimeout)
			c.Fix = "the keyring may be waiting for an unlock or \"allow\" prompt: run `gh auth token` once to answer it, or set GITHUB_TOKEN"
		default:
			c.Status, c.Detail = StatusProblem, "looking up the token failed: "+err.Error()
			c.Fix = "run `gh auth status` to see why gh cannot read its token, or set GITHUB_TOKEN"
		}
		r.add(c)
	}
}

func (r *runner) anthropicKey() {
	c := Check{ID: "anthropic-api-key", Label: "ANTHROPIC_API_KEY (anthropic backend)", Status: StatusInfo}
	switch {
	case r.opts.Getenv(config.EnvAnthropicAPIKey) != "":
		c.Status = StatusOK
		c.Detail = "set, so the anthropic backend is available: it calls the Anthropic API with this key, which is not tried here. auto uses it only when neither claude nor codex can run, and the claude and codex backends do not receive the key"
	case r.cfg.Backend == config.BackendAnthropic:
		c.Status = StatusProblem
		c.Detail = "not set, and the configured backend anthropic needs it"
		c.Fix = "export ANTHROPIC_API_KEY with your Anthropic API key, or choose another backend"
	default:
		c.Detail = "not set, so the anthropic backend is not available and auto skips it"
	}
	r.add(c)
}

func (r *runner) prices() {
	var fetchErr error
	if r.opts.FetchPrices {
		ctx, cancel := context.WithTimeout(r.ctx, priceTimeout)
		fetchErr = r.opts.RefreshPrices(ctx)
		cancel()
	}
	source := r.opts.PriceSource()
	c := Check{ID: "prices", Label: "price list", Version: source.Version, Source: source.Origin}
	const scope = "baum internal generate and the other commands use the embedded list (version %s); the baum daemon fetches the signed list when it starts and keeps the newest one in its state directory so queued jobs can resume. This check only shows whether fetching works and which version is published"
	when := ""
	if !source.FetchedAt.IsZero() {
		when = ", fetched " + source.FetchedAt.UTC().Format(time.RFC3339)
	}
	switch {
	case fetchErr != nil || source.RefreshError != "":
		reason := source.RefreshError
		if fetchErr != nil {
			reason = fetchErr.Error()
		}
		c.Status = StatusWarning
		c.Detail = fmt.Sprintf("fetching the signed price list failed: %s. The %s list (version %s) stays in use. ", reason, source.Origin, source.Version) + fmt.Sprintf(scope, pricing.Embedded().Version())
		c.Fix = "check your network connection and run baum internal doctor again; until then costs use the " + source.Origin + " list"
	case source.Origin == pricing.OriginFetched:
		c.Status = StatusOK
		c.Detail = fmt.Sprintf("fetching works: the published list is version %s%s. ", source.Version, when) + fmt.Sprintf(scope, pricing.Embedded().Version())
	case source.Origin == pricing.OriginCached:
		c.Status = StatusOK
		c.Detail = fmt.Sprintf("the signed list version %s that the baum daemon stored earlier is in use; the published list is no newer. ", source.Version) + fmt.Sprintf(scope, pricing.Embedded().Version())
		if !r.opts.FetchPrices {
			c.Status = StatusInfo
			c.Detail = fmt.Sprintf("not fetched (--offline); the signed list version %s that the baum daemon stored earlier is in use. ", source.Version) + fmt.Sprintf(scope, pricing.Embedded().Version())
		}
	case r.opts.FetchPrices:
		c.Status = StatusOK
		c.Detail = "fetching works: the published list is no newer than the embedded one. " + fmt.Sprintf(scope, pricing.Embedded().Version())
	default:
		c.Status = StatusInfo
		c.Detail = "not fetched (--offline); the embedded list is in use here. " + fmt.Sprintf(scope, pricing.Embedded().Version())
	}
	r.add(c)
}

// command reports the baum that coding-agent hooks run: the user-wide and
// committed hook commands call baum by name, so it is the first one on PATH
// (in the daemon, the login shell's PATH). It should be this baum, so that the
// hooks and the daemon they talk to are one version.
func (r *runner) command() {
	c := Check{ID: "baum-command", Label: "baum command", Source: "PATH"}
	path, err := r.opts.LookPath("baum")
	if err != nil {
		c.Status, c.Detail = StatusWarning, "baum is not on PATH, so coding-agent hooks cannot run it"
		c.Fix = "install the command from the desktop app (Command Line and Hooks… in its menu), or put the baum executable in a directory on your PATH"
		r.add(c)
		return
	}
	c.Path = path
	resolved, resolveErr := filepath.EvalSymlinks(path)
	self, selfErr := r.opts.Executable()
	if selfErr == nil {
		self, selfErr = filepath.EvalSymlinks(self)
	}
	if resolveErr == nil && selfErr == nil && samePath(resolved, self) {
		c.Status, c.Detail = StatusOK, "coding-agent hooks run this baum"
		if resolved != path {
			c.Detail += " (" + resolved + ")"
		}
		r.add(c)
		return
	}
	version, _, err := r.version(agent.LocalEnvironment(os.Environ()), path)
	if err == nil {
		c.Version = version
	}
	c.Status = StatusWarning
	c.Detail = "coding-agent hooks run " + path + ", a different baum from this one"
	if selfErr == nil {
		c.Detail += " (" + self + ")"
	}
	c.Fix = "replace it with the desktop app's baum (Command Line and Hooks… in its menu), or put this baum first on PATH, so that the hooks and the daemon run one version"
	r.add(c)
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (r *runner) environment() {
	value := r.opts.Getenv("PATH")
	c := Check{ID: "path", Label: "PATH", Status: StatusInfo}
	if value == "" {
		c.Status, c.Detail = StatusWarning, "PATH is empty, so git, gh, claude and codex cannot be found by name"
		c.Fix = "start baum from a shell, or set backend_paths.claude and backend_paths.codex in " + r.cfgPath + " (git must still be on PATH)"
		r.add(c)
		return
	}
	entries := filepath.SplitList(value)
	c.Detail = fmt.Sprintf("%d %s, searched in order: %s", len(entries), plural(len(entries), "entry", "entries"), strings.Join(entries, ", "))
	r.add(c)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func orUnknown(version string) string {
	if version == "" {
		return "(version not recognised)"
	}
	return version
}

func unwrapExec(err error) error {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr.Err
	}
	return err
}

// WriteText renders r for a terminal: one line per check with a status marker,
// then its details and fix, indented.
func WriteText(w io.Writer, r Report) error {
	var b strings.Builder
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%-9s %s\n", "["+string(c.Status)+"]", c.Label)
		for _, line := range strings.Split(c.Detail, "\n") {
			fmt.Fprintf(&b, "          %s\n", line)
		}
		var facts []string
		if c.Path != "" {
			facts = append(facts, "path: "+c.Path)
		}
		if c.Version != "" {
			facts = append(facts, "version: "+c.Version)
		}
		if c.Source != "" {
			facts = append(facts, "source: "+c.Source)
		}
		if len(facts) > 0 {
			fmt.Fprintf(&b, "          %s\n", strings.Join(facts, "; "))
		}
		if c.Fix != "" {
			fmt.Fprintf(&b, "          fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintf(&b, "\n%s: %d %s, %d %s\n", r.Status, r.Problems(), plural(r.Problems(), "problem", "problems"), r.Warnings(), plural(r.Warnings(), "warning", "warnings"))
	_, err := io.WriteString(w, b.String())
	return err
}
