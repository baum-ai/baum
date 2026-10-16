// Package doctor reports why baum can or cannot find its tools, backend and
// GitHub token, with a fix for each problem.
//
// It runs "<tool> --version" for git, gh, claude, codex and cursor-agent,
// the same checks "baum generate" runs for claude and codex (including
// codex's read-only sandbox preflight), "gh auth token" when no token is
// configured, and at most one network request, for the signed price list. It
// never prints a token or an API key, only where one was found.
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
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/pricing"
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
	// Run runs path with args and returns its combined output, under ctx.
	Run func(ctx context.Context, path string, args ...string) (string, error)
	// Backends builds the backends to check; nil builds the real ones from
	// the configuration, as "baum generate" does.
	Backends func(config.Config) generate.Backends
	// GitHubToken resolves the token for host; nil uses the configuration's
	// own resolver.
	GitHubToken func(ctx context.Context, cfg config.Config, host string) (token, source string, err error)
	// RefreshPrices fetches the signed list; nil uses pricing.Refresh.
	RefreshPrices func(ctx context.Context) error
	// PriceSource describes the active list; nil uses pricing.CurrentSource.
	PriceSource func() pricing.Source
	// Registry lists the registered clones of the registry file at path. It
	// is called only when that file exists, because opening one creates it.
	Registry func(ctx context.Context, path string) ([]repos.Clone, error)
	// InstanceDir is the directory of the per-user "baum ui" instance and its
	// capability files; nil omits that check. The caller supplies it because
	// the UI server may itself serve this report.
	InstanceDir func() (string, error)
}

// Timeouts bound every subprocess and network request the report makes.
const (
	versionTimeout = 10 * time.Second
	backendTimeout = 90 * time.Second // codex: version, then two sandbox runs
	tokenTimeout   = 30 * time.Second
	priceTimeout   = 15 * time.Second
	registryTime   = 30 * time.Second
)

var versionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

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
	ctx     context.Context
	opts    Options
	checks  []Check
	cfg     config.Config
	cfgPath string
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
		o.RefreshPrices = pricing.Refresh
	}
	if o.PriceSource == nil {
		o.PriceSource = pricing.CurrentSource
	}
	if o.Registry == nil {
		o.Registry = listClones
	}
}

func runCommand(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if len(out) > 4096 {
		out = out[:4096]
	}
	return string(out), err
}

func listClones(ctx context.Context, path string) ([]repos.Clone, error) {
	registry, err := repos.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = registry.Close() }()
	return registry.List(ctx)
}

func (r *runner) add(c Check) { r.checks = append(r.checks, c) }

func (r *runner) run() {
	r.cfgPath, _ = config.Path()
	if r.cfgPath == "" {
		r.cfgPath = "config.toml"
	}
	cfg, cfgErr := config.Load(config.Overrides{})
	if cfgErr != nil {
		cfg = config.Default()
	}
	r.cfg = cfg

	r.git()
	r.gh()
	checked := r.backends()
	r.cursorAgent()
	r.selection(checked)
	clones := r.locations(cfgErr)
	r.tokens(clones)
	r.anthropicKey()
	r.prices()
	r.environment()
}

// tool resolves an executable and its version. explicit is a configured
// absolute path, which replaces the PATH lookup. found is false when there is
// no usable executable; c then holds the failure's wording in Detail.
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

func (r *runner) version(ctx context.Context, path string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := r.opts.Run(ctx, path, args...)
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
	version, out, err := r.version(r.ctx, path, "--version")
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
	version, _, err := r.version(r.ctx, path, "--version")
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
	config.BackendClaude: "Claude Code",
	config.BackendCodex:  "Codex",
}

// checkResult is what a backend's Check returned.
type checkResult struct {
	version string
	err     error
}

// checkedBackends holds the claude and codex results, which selection replays.
type checkedBackends struct{ claude, codex checkResult }

// backends checks claude and codex with the checks "baum generate" runs, once
// each; selection replays the results instead of running them again.
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
	case lookErr != nil:
		c.Status, c.Detail = severity, tool+" was not found on PATH"
		c.Fix = fmt.Sprintf("install %s and sign in, or set backend_paths.%s to its absolute path in %s (apps started without a shell's PATH need this)", tool, name, r.cfgPath)
	default:
		c.Status, c.Detail = severity, err.Error()
		c.Fix = checkFix(name, tool, path, err)
		if v, _, verr := r.version(r.ctx, path, "--version"); verr == nil {
			c.Version = v
		}
	}
	r.add(c)
	return checkResult{version, err}
}

func checkFix(name config.Backend, tool, path string, err error) string {
	switch msg := err.Error(); {
	case strings.Contains(msg, "older than"):
		return "upgrade " + tool + " to the version named above or newer"
	case strings.Contains(msg, "sandbox"):
		return "baum runs codex only inside its read-only sandbox; run `" + path + " sandbox -P :read-only -- cat /dev/null` to see why it fails (on Linux it needs user namespaces), or use claude"
	}
	return fmt.Sprintf("run `%s --version` yourself to see why it fails, or reinstall %s", path, tool)
}

func (r *runner) cursorAgent() {
	const name = "cursor-agent"
	c := Check{ID: name, Label: name}
	explicit := r.cfg.BackendPaths[config.BackendCursorAgent]
	path, source, err := r.tool(name, explicit)
	c.Source = source
	if err != nil {
		if explicit != "" {
			c.Status, c.Detail = StatusProblem, err.Error()
			c.Fix = fmt.Sprintf("fix backend_paths.%s in %s, or remove it to search PATH", name, r.cfgPath)
		} else {
			c.Status, c.Detail = StatusInfo, "cursor-agent was not found on PATH; the cursor-agent backend is not available in this version of baum"
		}
		r.add(c)
		return
	}
	c.Path = path
	version, _, err := r.version(r.ctx, path, "--version")
	c.Version = version
	c.Status = StatusInfo
	c.Detail = "found, but the cursor-agent backend is not available in this version of baum"
	if err != nil {
		c.Detail += fmt.Sprintf(" (running %s --version failed: %v)", path, err)
	}
	r.add(c)
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

	var skipped bytes.Buffer
	selected, err := generate.Select(r.ctx, config.BackendAuto, r.cfg.Model, replayed, &skipped)
	c := Check{ID: "backend-auto", Label: "backend: auto"}
	reasons := strings.TrimSpace(skipped.String())
	if err != nil {
		c.Status, c.Detail = StatusProblem, "auto finds no usable backend: "+err.Error()
		c.Fix = fmt.Sprintf("install Claude Code or Codex, or set backend_paths.claude or backend_paths.codex to its absolute path in %s; see the claude and codex checks above", r.cfgPath)
	} else {
		c.Status, c.Version = StatusOK, selected.Version
		c.Detail = fmt.Sprintf("auto picks %s, the first of claude and codex that is installed and passes its checks", selected.Backend.Name())
		if reasons != "" {
			c.Detail += "\n" + reasons
		}
	}
	r.add(c)

	if r.cfg.Backend == config.BackendAuto {
		return
	}
	configured := Check{ID: "backend-configured", Label: "backend: " + string(r.cfg.Backend)}
	selected, err = generate.Select(r.ctx, r.cfg.Backend, r.cfg.Model, replayed, nil)
	if err != nil {
		configured.Status, configured.Detail = StatusProblem, fmt.Sprintf("the configured backend %s cannot run: %v", r.cfg.Backend, err)
		configured.Fix = fmt.Sprintf("fix the %s check above, or set backend = \"auto\", \"claude\" or \"codex\" in %s (BAUM_BACKEND and --backend override it)", r.cfg.Backend, r.cfgPath)
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
	switch _, statErr := os.Stat(r.cfgPath); {
	case cfgErr != nil:
		cfg.Status, cfg.Detail = StatusProblem, "the configuration does not load: "+cfgErr.Error()
		cfg.Fix = fmt.Sprintf("correct the setting named above in %s (or the BAUM_BACKEND environment variable), or remove it; the other checks use default settings", r.cfgPath)
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
			c.Detail = "not created yet; no clones are registered (baum repos add <path>)"
		} else {
			ctx, cancel := context.WithTimeout(r.ctx, registryTime)
			var listErr error
			clones, listErr = r.opts.Registry(ctx, registryPath)
			cancel()
			if listErr != nil {
				c.Status, c.Detail = StatusWarning, "cannot read the registry: "+listErr.Error()
				c.Fix = "check that " + registryPath + " is readable and not locked by another baum process"
			} else {
				c.Detail = fmt.Sprintf("%d registered %s", len(clones), plural(len(clones), "clone", "clones"))
			}
		}
		r.add(c)
	}

	r.location("jobs", "job queue", jobs.DefaultPath, "created when baum ui runs its first job")
	r.location("discovery", "discovery cache", discovery.DefaultDirectory, "created when baum ui first looks for pull requests")
	if r.opts.InstanceDir != nil {
		r.location("instance", "baum ui instance directory", r.opts.InstanceDir, "created when baum ui starts")
	}

	for _, clone := range clones {
		label := "store: " + clone.Repository
		if !clone.Available {
			r.add(Check{ID: "store-" + clone.ID, Label: label, Path: clone.Path, Status: StatusWarning,
				Detail: "the clone is not available at its registered path, so its store is not read",
				Fix:    fmt.Sprintf("restore the clone at %s, or run `baum repos remove %s`", clone.Path, clone.ID)})
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
		ctx, cancel := context.WithTimeout(r.ctx, tokenTimeout)
		_, source, err := r.opts.GitHubToken(ctx, r.cfg, host)
		cancel()
		switch {
		case err == nil:
			c.Status, c.Source = StatusOK, source
			c.Detail = "a token is available for " + host
		case errors.Is(err, config.ErrNoGitHubToken):
			_, fix, _ := strings.Cut(err.Error(), ": ")
			c.Status, c.Fix = StatusWarning, fix
			c.Detail = "no token was found for " + host
			if host == config.GitHubHost {
				c.Detail += "; public repositories work without one, within GitHub's low unauthenticated rate limit"
			} else {
				c.Status = StatusProblem
				c.Detail += ", and a registered clone belongs to it"
			}
		default:
			c.Status, c.Detail = StatusProblem, "looking up the token failed: "+err.Error()
			c.Fix = "run `gh auth status` to see why gh cannot read its token, or set GITHUB_TOKEN"
		}
		r.add(c)
	}
}

func (r *runner) anthropicKey() {
	c := Check{ID: "anthropic-api-key", Label: "ANTHROPIC_API_KEY", Status: StatusInfo}
	if r.opts.Getenv(config.EnvAnthropicAPIKey) != "" {
		c.Detail = "set. The anthropic backend is not available in this version of baum, and the claude and codex backends do not receive the key"
	} else {
		c.Detail = "not set. The anthropic backend is not available in this version of baum, so nothing needs it"
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
	const noCache = "baum keeps no price list on disk; each process starts with the embedded list"
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
		c.Detail = fmt.Sprintf("refreshing the signed price list failed: %s. The %s list (version %s) is used", reason, source.Origin, source.Version)
		c.Fix = "check your network connection and run baum doctor again; until then costs use the " + source.Origin + " list"
	case source.Origin == pricing.OriginFetched:
		c.Status = StatusOK
		c.Detail = "using the signed list fetched from the published location" + when + ". " + noCache
	case r.opts.FetchPrices:
		c.Status = StatusOK
		c.Detail = "the signed list was fetched and is no newer than the embedded list, which stays in use. " + noCache
	default:
		c.Status = StatusInfo
		c.Detail = "using the embedded list; run without --offline to fetch the signed list. " + noCache
	}
	r.add(c)
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
