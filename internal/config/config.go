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
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Environment variables read by Load.
const (
	EnvGitHubToken     = "GITHUB_TOKEN"
	EnvGHToken         = "GH_TOKEN"
	EnvAnthropicAPIKey = "ANTHROPIC_API_KEY"
	EnvBackend         = "BAUM_BACKEND"
	EnvModel           = "BAUM_MODEL"
)

// Backend names a model backend. Auto picks one at run time, see Detect.
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
	// AnthropicAPIKey comes from the environment only and is never written to
	// the file.
	AnthropicAPIKey string  `toml:"-"`
	Budgets         Budgets `toml:"budgets"`
	// Caps override the generation bundle caps by name; unnamed caps keep their
	// defaults. Names are defined by the bundle assembler.
	Caps map[string]int `toml:"caps,omitempty"`
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
		cfg.GitHubToken = v
	} else if v := getenv(EnvGHToken); v != "" {
		cfg.GitHubToken = v
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
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Backend == "" {
		cfg.Backend = BackendAuto
	}
	return cfg, true, nil
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

// ghAuthToken asks the GitHub CLI for the token of its active account. A
// variable so tests can stand in for the binary.
var ghAuthToken = func(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ErrNoGitHubToken is returned when no token is configured and gh has none.
var ErrNoGitHubToken = errors.New("no GitHub token: set GITHUB_TOKEN, add github_token to the config file, or run `gh auth login`")

// ResolveGitHubToken returns the configured token, falling back to
// `gh auth token` when none is set.
func (c Config) ResolveGitHubToken(ctx context.Context) (string, error) {
	if c.GitHubToken != "" {
		return c.GitHubToken, nil
	}
	token, err := ghAuthToken(ctx)
	if err != nil || token == "" {
		return "", ErrNoGitHubToken
	}
	return token, nil
}

// ErrNoBackend is returned by Detect when nothing usable is available.
var ErrNoBackend = errors.New("no model backend available: install claude, codex or cursor-agent, or set ANTHROPIC_API_KEY")

// Detect resolves BackendAuto to a concrete backend: the first of claude,
// codex and cursor-agent found on PATH, then anthropic when an API key is
// set. Any other backend is returned unchanged.
func (c Config) Detect() (Backend, error) {
	return c.detect(exec.LookPath)
}

func (c Config) detect(lookPath func(string) (string, error)) (Backend, error) {
	if c.Backend != BackendAuto {
		return c.Backend, nil
	}
	for _, b := range []Backend{BackendClaude, BackendCodex, BackendCursorAgent} {
		if _, err := lookPath(string(b)); err == nil {
			return b, nil
		}
	}
	if c.AnthropicAPIKey != "" {
		return BackendAnthropic, nil
	}
	return "", ErrNoBackend
}
