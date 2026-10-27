// Package config resolves baum settings from flags, the environment,
// the XDG config file and defaults, in that order of precedence.
//
// Local review commands contact no baum server and send no telemetry; the file holds the
// backend choice, optional tokens and generation budgets, nothing else.
package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/baum-ai/baum/internal/githubauth"
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
// codex, each when installed and passing its checks, then anthropic when
// ANTHROPIC_API_KEY is set. cursor-agent runs only when named.
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
	// AgentEnv are variables set for every agent baum runs on this computer
	// and for the git commands that commit and push a fix, over the inherited
	// environment: for example GIT_CONFIG_GLOBAL, to sign an agent's commits
	// with another key. The app's own choice replaces it (repos.Preferences).
	AgentEnv map[string]string `toml:"agent_env,omitempty"`
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

// MaxAgentEnv bounds how many variables AgentEnv may set.
const MaxAgentEnv = 32

// ValidateAgentEnv rejects a name that is not a portable variable name, a
// value with a NUL byte or longer than 4 KiB, and more than MaxAgentEnv
// variables.
func ValidateAgentEnv(env map[string]string) error {
	if len(env) > MaxAgentEnv {
		return fmt.Errorf("at most %d variables", MaxAgentEnv)
	}
	names := slices.Sorted(maps.Keys(env))
	for _, name := range names {
		if !validEnvName(name) {
			return fmt.Errorf("%q is not a variable name; use letters, digits and underscores, not starting with a digit", name)
		}
		if value := env[name]; len(value) > 4<<10 || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%s: the value must be at most 4 KiB, without NUL bytes", name)
		}
	}
	return nil
}

func validEnvName(name string) bool {
	if name == "" || len(name) > 256 || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for _, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// AgentEnviron is AgentEnv as "NAME=value" entries sorted by name, each
// value with a leading ~ and $NAME or ${NAME} expanded from this process's
// environment, as a shell would.
func (c Config) AgentEnviron() []string {
	if len(c.AgentEnv) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	env := make([]string, 0, len(c.AgentEnv))
	for _, name := range slices.Sorted(maps.Keys(c.AgentEnv)) {
		value := c.AgentEnv[name]
		if home != "" && (value == "~" || strings.HasPrefix(value, "~/")) {
			value = home + value[1:]
		}
		env = append(env, name+"="+os.ExpandEnv(value))
	}
	return env
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
	if err := ValidateAgentEnv(cfg.AgentEnv); err != nil {
		return Config{}, false, fmt.Errorf("%s: agent_env: %w", path, err)
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
	name, ok := durationKey(key)
	if value.Kind == unstable.InlineTable && !ok {
		children := value.Children()
		for children.Next() {
			if err := checkDurationNode(p, path, key, children.Node()); err != nil {
				return err
			}
		}
		return nil
	}
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

// GitHubAuth is shared by every token consumer and the local authentication API.
var GitHubAuth = githubauth.New(githubauth.ProcessStore{})

const SourceBaum = "baum credential store"

var ErrNoGitHubToken = errors.New("no GitHub token")

// ExplicitGitHubTokenSource resolves overrides without opening the keyring.
// Enterprise credentials apply only to the explicitly selected GH_HOST.
func (c Config) ExplicitGitHubTokenSource(host string) (string, string) {
	host = auth.NormalizeHostname(strings.TrimSpace(host))
	if host == GitHubHost {
		if c.GitHubToken != "" {
			if c.GitHubTokenSource == EnvGitHubToken || c.GitHubTokenSource == EnvGHToken {
				return c.GitHubToken, "environment variable " + c.GitHubTokenSource
			}
			return c.GitHubToken, SourceConfigFile
		}
		for _, name := range []string{EnvGitHubToken, EnvGHToken} {
			if token := strings.TrimSpace(os.Getenv(name)); token != "" {
				return token, "environment variable " + name
			}
		}
	} else if host != "" && host == auth.NormalizeHostname(strings.TrimSpace(os.Getenv("GH_HOST"))) {
		names := []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
		if auth.IsTenancy(host) {
			names = []string{EnvGHToken, EnvGitHubToken}
		}
		for _, name := range names {
			if token := strings.TrimSpace(os.Getenv(name)); token != "" {
				return token, "environment variable " + name
			}
		}
	}
	return "", ""
}

func (c Config) ResolveGitHubToken(ctx context.Context, host string) (string, error) {
	token, _, err := c.ResolveGitHubTokenSource(ctx, host)
	return token, err
}

func (c Config) ResolveGitHubTokenSource(ctx context.Context, host string) (string, string, error) {
	host = auth.NormalizeHostname(strings.TrimSpace(host))
	if token, source := c.ExplicitGitHubTokenSource(host); token != "" {
		return token, source, nil
	}
	if host == GitHubHost {
		token, err := GitHubAuth.Token(ctx)
		if err == nil {
			return token, SourceBaum, nil
		}
		if !errors.Is(err, githubauth.ErrNotConnected) {
			return "", "", err
		}
		return "", "", fmt.Errorf("%w for github.com: connect GitHub in baum Settings, set GITHUB_TOKEN, or add github_token to the config file", ErrNoGitHubToken)
	}
	return "", "", fmt.Errorf("%w: set GH_HOST to the repository host and GH_ENTERPRISE_TOKEN for Enterprise access", ErrNoGitHubToken)
}
