// Package config resolves baum settings from flags, the environment,
// the XDG config file and defaults, in that order of precedence.
//
// baum has no server, no login and no telemetry; the file holds the
// backend choice, optional tokens and generation budgets, nothing else.
package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/cli/safeexec"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Environment variables read by Load.
const (
	EnvGitHubToken     = "GITHUB_TOKEN"
	EnvGHToken         = "GH_TOKEN"
	EnvAnthropicAPIKey = "ANTHROPIC_API_KEY"
	EnvBackend         = "BAUM_BACKEND"
	EnvModel           = "BAUM_MODEL"
)

// SourceConfigFile is the GitHubTokenSource of github_token in the config file.
const SourceConfigFile = "config file"

// GitHubHost is github.com, the host GITHUB_TOKEN, GH_TOKEN and github_token
// belong to.
const GitHubHost = "github.com"

// Backend names a model backend. Auto picks one at run time: claude, then
// codex, each when installed and passing its checks.
type Backend string

const (
	BackendAuto        Backend = "auto"
	BackendAnthropic   Backend = "anthropic"
	BackendClaude      Backend = "claude"
	BackendCodex       Backend = "codex"
	BackendCursorAgent Backend = "cursor-agent"
)

// Backends lists every accepted backend name, Auto first.
var Backends = []Backend{BackendAuto, BackendAnthropic, BackendClaude, BackendCodex, BackendCursorAgent}

// Valid reports whether b is one of Backends.
func (b Backend) Valid() bool {
	for _, known := range Backends {
		if b == known {
			return true
		}
	}
	return false
}

// ParseBackend accepts a backend name case-insensitively.
func ParseBackend(s string) (Backend, error) {
	b := Backend(strings.ToLower(strings.TrimSpace(s)))
	if !b.Valid() {
		return "", fmt.Errorf("unknown backend %q; use one of %s", s, joinBackends())
	}
	return b, nil
}

func joinBackends() string {
	names := make([]string, len(Backends))
	for i, b := range Backends {
		names[i] = string(b)
	}
	return strings.Join(names, ", ")
}

// Duration is a time.Duration written in TOML as a string such as "10m".
type Duration time.Duration

// MarshalText renders d with time.Duration's String.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// UnmarshalText parses d with time.ParseDuration.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// Budgets bound one generate run. Exceeding a budget fails the run naming it.
type Budgets struct {
	PassTimeout Duration `toml:"pass_timeout"`
	RunTimeout  Duration `toml:"run_timeout"`
	MaxCalls    int      `toml:"max_calls"`
	MaxTokens   int      `toml:"max_tokens"`
}

// Config is the resolved configuration. Its TOML form is the on-disk file.
type Config struct {
	Backend     Backend `toml:"backend"`
	Model       string  `toml:"model,omitempty"`
	GitHubToken string  `toml:"github_token,omitempty"`
	// GitHubTokenSource names where GitHubToken came from: GITHUB_TOKEN,
	// GH_TOKEN or the config file. It is empty when there is no token and is
	// never written to the file.
	GitHubTokenSource string `toml:"-"`
	// AnthropicAPIKey comes from the environment only and is never written to
	// the file.
	AnthropicAPIKey string  `toml:"-"`
	Budgets         Budgets `toml:"budgets"`
	// Caps override the generation bundle caps by name; unnamed caps keep their
	// defaults. Names are defined by the bundle assembler.
	Caps map[string]int `toml:"caps,omitempty"`
	// BackendPaths names the executable of a backend by absolute path. A named
	// path is used instead of a PATH lookup, never in addition to one, so apps
	// started without the shell's PATH still find their backends. Only claude,
	// codex and cursor-agent are accepted; per-repository settings do not
	// override it.
	BackendPaths map[Backend]string `toml:"backend_paths,omitempty"`
}

// PathBackends are the backends whose executable BackendPaths can name.
var PathBackends = []Backend{BackendClaude, BackendCodex, BackendCursorAgent}

// validateBackendPaths rejects a key that is not an executable backend and a
// value that is not an absolute path, naming the key and the fix.
func validateBackendPaths(paths map[Backend]string) error {
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, string(key))
	}
	slices.Sort(keys)
	for _, name := range keys {
		if !slices.Contains(PathBackends, Backend(name)) {
			return fmt.Errorf("backend_paths.%s: unknown backend; use claude, codex or cursor-agent as the key", name)
		}
		if path := paths[Backend(name)]; !filepath.IsAbs(path) {
			return fmt.Errorf("backend_paths.%s = %q is not an absolute path; write the full path of the executable, for example backend_paths.%s = %q", name, path, name, examplePath(name))
		}
	}
	return nil
}

func examplePath(name string) string {
	if runtime.GOOS == "windows" {
		return `C:\tools\` + name + ".exe"
	}
	return "/usr/local/bin/" + name
}

// Default is the configuration with nothing set anywhere.
func Default() Config {
	return Config{
		Backend: BackendAuto,
		Budgets: Budgets{
			PassTimeout: Duration(10 * time.Minute),
			RunTimeout:  Duration(45 * time.Minute),
			MaxCalls:    40,
			MaxTokens:   2_000_000,
		},
	}
}

// Overrides are the flag values, which take precedence over everything else.
// Empty strings mean "not given".
type Overrides struct {
	Backend string
	Model   string
	// Path replaces the default config file location.
	Path string
}

// Path returns the config file location: $XDG_CONFIG_HOME/baum/config.toml,
// defaulting to ~/.config/baum/config.toml.
func Path() (string, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "baum", "config.toml"), nil
}

// Load resolves the configuration with the precedence flags, environment,
// file, defaults. A missing file is not an error.
func Load(overrides Overrides) (Config, error) {
	return load(overrides, os.Getenv)
}

func load(overrides Overrides, getenv func(string) string) (Config, error) {
	path := overrides.Path
	if path == "" {
		var err error
		if path, err = Path(); err != nil {
			return Config{}, err
		}
	}
	cfg, _, err := ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	if v := getenv(EnvBackend); v != "" {
		cfg.Backend = Backend(v)
	}
	if v := getenv(EnvModel); v != "" {
		cfg.Model = v
	}
	if v := getenv(EnvGitHubToken); v != "" {
		cfg.GitHubToken, cfg.GitHubTokenSource = v, EnvGitHubToken
	} else if v := getenv(EnvGHToken); v != "" {
		cfg.GitHubToken, cfg.GitHubTokenSource = v, EnvGHToken
	}
	cfg.AnthropicAPIKey = getenv(EnvAnthropicAPIKey)

	if overrides.Backend != "" {
		cfg.Backend = Backend(overrides.Backend)
	}
	if overrides.Model != "" {
		cfg.Model = overrides.Model
	}

	backend, err := ParseBackend(string(cfg.Backend))
	if err != nil {
		return Config{}, err
	}
	cfg.Backend = backend
	return cfg, nil
}

// ReadFile parses the config file at path over the defaults. ok is false when
// the file does not exist.
func ReadFile(path string) (cfg Config, ok bool, err error) {
	cfg = Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("reading config: %w", err)
	}
	if err := checkDurationTypes(path, data); err != nil {
		return Config{}, false, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, parseError(path, err)
	}
	if cfg.Backend == "" {
		cfg.Backend = BackendAuto
	}
	if err := validateBackendPaths(cfg.BackendPaths); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.GitHubToken != "" {
		cfg.GitHubTokenSource = SourceConfigFile
	}
	return cfg, true, nil
}

// durationKeys are the dotted names of every Duration field in the file.
var durationKeys = []string{"budgets.pass_timeout", "budgets.run_timeout"}

// checkDurationTypes rejects a Duration key written as anything but a string.
// go-toml decodes an integer into a Duration as raw nanoseconds, so
// run_timeout = 10 would mean ten nanoseconds and fail the run's budget at
// once; the decoder cannot tell it from "10ns". Syntax errors are left to
// toml.Unmarshal.
func checkDurationTypes(path string, data []byte) error {
	var p unstable.Parser
	p.Reset(data)
	var table []string
	for p.NextExpression() {
		expr := p.Expression()
		switch expr.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = keyParts(expr.Key())
		case unstable.KeyValue:
			if err := checkDurationNode(&p, path, table, expr); err != nil {
				return err
			}
		}
	}
	return nil
}

func keyParts(it unstable.Iterator) []string {
	var parts []string
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	return parts
}

// checkDurationNode checks one key-value under prefix, recursing into inline
// tables.
func checkDurationNode(p *unstable.Parser, path string, prefix []string, kv *unstable.Node) error {
	key := append(slices.Clone(prefix), keyParts(kv.Key())...)
	value := kv.Value()
	if value.Kind == unstable.InlineTable {
		children := value.Children()
		for children.Next() {
			if err := checkDurationNode(p, path, key, children.Node()); err != nil {
				return err
			}
		}
		return nil
	}
	name, ok := durationKey(key)
	if value.Kind == unstable.String || !ok {
		return nil
	}
	shape := p.Shape(value.Raw)
	return fmt.Errorf(`parsing %s: wrong type or value for %s at line %d, column %d: a duration is a string; write it like "10m"`, path, name, shape.Start.Line, shape.Start.Column)
}

// durationKey returns the durationKeys name that key decodes into. go-toml
// matches keys to fields without regard to case, so the parts are compared the
// same way; a quoted key containing a dot is one part and matches nothing.
func durationKey(key []string) (string, bool) {
next:
	for _, name := range durationKeys {
		parts := strings.Split(name, ".")
		if len(parts) != len(key) {
			continue
		}
		for i, part := range parts {
			if !strings.EqualFold(part, key[i]) {
				continue next
			}
		}
		return name, true
	}
	return "", false
}

// parseError reports a failure to read the file by position and key, never by
// the text it held: go-toml's String quotes source lines and its syntax errors
// name a character, and a line may hold a token. A syntax error has no key; a
// type or value error names the key, and the expected type where the message
// carries no value. github_token's error never says more than its key.
func parseError(path string, err error) error {
	var decodeErr *toml.DecodeError
	if !errors.As(err, &decodeErr) {
		return fmt.Errorf("parsing %s: invalid TOML; values are not shown, in case one is a token", path)
	}
	line, column := decodeErr.Position()
	key := decodeErr.Key()
	if len(key) == 0 {
		return fmt.Errorf("parsing %s: invalid TOML syntax at line %d, column %d; fix the syntax there (values are not shown, in case one is a token)", path, line, column)
	}
	name := strings.Join(key, ".")
	message := fmt.Errorf("parsing %s: wrong type or value for %s at line %d, column %d", path, name, line, column)
	if key[len(key)-1] == "github_token" {
		return message
	}
	switch reason := strings.TrimPrefix(decodeErr.Error(), "toml: "); {
	case strings.HasPrefix(reason, "cannot decode TOML "), strings.HasSuffix(reason, "is already defined"):
		// Type mismatches and duplicates name types and keys, not values.
		return fmt.Errorf("%w: %s", message, reason)
	case name == "budgets.pass_timeout" || name == "budgets.run_timeout":
		return fmt.Errorf(`%w: invalid duration; write it like "10m"`, message)
	}
	return fmt.Errorf("%w: invalid value", message)
}

// Save writes cfg to path, creating the directory 0700 and the file 0600 so a
// stored token is readable by the user only.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	// WriteFile honours the umask and leaves an existing file's mode alone.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("setting config permissions: %w", err)
	}
	return nil
}

// ghToken is the GitHub CLI's token for host, as gh resolves it: GH_TOKEN and
// GITHUB_TOKEN for github.com and GitHub Enterprise Cloud with data
// residency (*.ghe.com), GH_ENTERPRISE_TOKEN and GITHUB_ENTERPRISE_TOKEN for
// an Enterprise Server, then gh's hosts.yml, then the system keyring. source
// says which of them answered. A variable so tests can stand in for it.
var ghToken = func(ctx context.Context, host string) (token, source string, err error) {
	if token, name := auth.TokenFromEnvOrConfig(host); token != "" {
		return token, ghSourceLabel(name), nil
	}
	token, err = ghKeyringToken(ctx, host)
	if token != "" {
		source = SourceGHKeyring
	}
	return token, source, err
}

// Sources of a token that gh resolved, as ResolveGitHubTokenSource reports
// them. An environment variable is "environment variable NAME".
const (
	SourceGHHostsFile = "gh hosts.yml"
	SourceGHKeyring   = "gh keyring (gh auth token)"
)

// ghSourceLabel turns go-gh's source, an environment variable name or the
// hosts.yml key, into a label.
func ghSourceLabel(name string) string {
	if strings.HasSuffix(name, "_TOKEN") {
		return "environment variable " + name
	}
	return SourceGHHostsFile
}

// ghKeyringWaitDelay bounds how long a cancelled `gh auth token` may keep
// its output open, for instance through a helper it started.
const ghKeyringWaitDelay = time.Second

// ghKeyringToken asks gh for host's token from the system keyring, as go-gh
// does, but under ctx: a keyring that waits for the user to unlock it must
// not outlive a cancelled command. gh is GH_PATH or the gh on PATH; no gh, or
// no token, is "".
func ghKeyringToken(ctx context.Context, host string) (string, error) {
	gh := os.Getenv("GH_PATH")
	if gh == "" {
		var err error
		if gh, err = safeexec.LookPath("gh"); err != nil {
			return "", nil
		}
	}
	cmd := exec.CommandContext(ctx, gh, "auth", "token", "--secure-storage", "--hostname", host)
	cmd.WaitDelay = ghKeyringWaitDelay
	out, err := cmd.Output()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("asking gh for the token of %s: %w", host, ctxErr)
	}
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// ErrNoGitHubToken is returned when no token is configured for a host and gh
// has none either.
var ErrNoGitHubToken = errors.New("no GitHub token")

// ResolveGitHubToken returns the token for the GitHub host whose repository
// is read, and never another host's. For github.com baum's own sources come
// first: GITHUB_TOKEN, GH_TOKEN, then github_token in the config file. After
// them, and for any other host only, the GitHub CLI's token for that host
// (see ghToken). Cancelling ctx stops a keyring lookup still waiting.
func (c Config) ResolveGitHubToken(ctx context.Context, host string) (string, error) {
	token, _, err := c.ResolveGitHubTokenSource(ctx, host)
	return token, err
}

// ResolveGitHubTokenSource is ResolveGitHubToken that also says where the
// token came from: "environment variable GITHUB_TOKEN" (or GH_TOKEN),
// "config file", gh's environment variable, "gh hosts.yml" or
// "gh keyring (gh auth token)". The label never holds any part of the token.
func (c Config) ResolveGitHubTokenSource(ctx context.Context, host string) (token, source string, err error) {
	host = auth.NormalizeHostname(strings.TrimSpace(host))
	if host == "" {
		return "", "", fmt.Errorf("%w: no host given", ErrNoGitHubToken)
	}
	if host == GitHubHost && c.GitHubToken != "" {
		switch c.GitHubTokenSource {
		case EnvGitHubToken, EnvGHToken:
			return c.GitHubToken, "environment variable " + c.GitHubTokenSource, nil
		}
		return c.GitHubToken, SourceConfigFile, nil
	}
	token, source, err = ghToken(ctx, host)
	if err != nil {
		return "", "", err
	}
	if token = strings.TrimSpace(token); token != "" {
		return token, source, nil
	}
	if host == GitHubHost {
		return "", "", fmt.Errorf("%w for %s: set GITHUB_TOKEN, add github_token to the config file, or run `gh auth login`", ErrNoGitHubToken, host)
	}
	return "", "", fmt.Errorf("%w for %s: set GH_ENTERPRISE_TOKEN or run `gh auth login --hostname %s`", ErrNoGitHubToken, host, host)
}
