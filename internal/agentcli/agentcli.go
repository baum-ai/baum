// Package agentcli finds the coding-agent CLIs baum runs (Claude Code and
// Codex), checks their versions against the oldest each backend supports, and
// keeps the record `baum desktop-setup` writes from the user's terminal.
//
// Some users put an agent on their PATH only in an interactive shell (a
// version manager set up in ~/.bashrc), which baum cannot start without a
// terminal, so it finds an older copy or none. The record names the
// directories the user's terminal found the agents in; baum puts them first
// on the PATH it uses (WithRecorded), so every lookup of `claude` or `codex`,
// and the `node` an npm-installed agent starts, finds what the user runs.
package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/config"
)

// Agent is a coding-agent CLI baum runs.
type Agent struct {
	// Slug names the agent as `baum hooks` does.
	Slug string
	Name string
	// Binary is the command's name on the PATH.
	Binary string
	// Minimum is the oldest version baum runs.
	Minimum string
}

// Agents are the CLIs baum checks, in the order it reports them.
var Agents = []Agent{
	{Slug: "claude-code", Name: "Claude Code", Binary: "claude", Minimum: claude.MinimumVersion},
	{Slug: "codex", Name: "Codex", Binary: "codex", Minimum: codex.MinimumVersion},
}

// Lookup returns the agent with slug.
func Lookup(slug string) (Agent, bool) {
	i := slices.IndexFunc(Agents, func(a Agent) bool { return a.Slug == slug })
	if i < 0 {
		return Agent{}, false
	}
	return Agents[i], true
}

// versionTimeout bounds one `--version`.
const versionTimeout = 15 * time.Second

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Version runs binary --version and returns the first dotted version it
// prints.
func Version(ctx context.Context, binary string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running %s --version: %w", binary, err)
	}
	version := versionPattern.FindString(string(out))
	if version == "" {
		return "", fmt.Errorf("%s --version printed no version", binary)
	}
	return version, nil
}

// Supported reports whether version is minimum or newer. An unreadable
// version is not supported.
func Supported(version, minimum string) bool {
	v, m := "v"+version, "v"+minimum
	return semver.IsValid(v) && semver.IsValid(m) && semver.Compare(v, m) >= 0
}

// Found is what Find learned about an agent on the PATH.
type Found struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
	// Path is where the PATH lookup found it; empty when it did not.
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Minimum string `json:"minimum_version"`
	// Supported is whether Version is Minimum or newer.
	Supported bool `json:"supported"`
	// Error is why the version could not be read.
	Error string `json:"error,omitempty"`
}

// Find looks agent up on the current PATH and reads its version.
func Find(ctx context.Context, agent Agent) Found {
	found := Found{Agent: agent.Slug, Name: agent.Name, Minimum: agent.Minimum}
	path, err := exec.LookPath(agent.Binary)
	if err != nil {
		return found
	}
	found.Path = path
	found.Version, err = Version(ctx, path)
	if err != nil {
		found.Error = err.Error()
	}
	found.Supported = Supported(found.Version, agent.Minimum)
	return found
}

// Record is what `baum desktop-setup` saw on the user's terminal PATH.
type Record struct {
	Agents []Recorded `json:"agents"`
}

// Recorded is one agent found on the terminal's PATH.
type Recorded struct {
	Agent string `json:"agent"`
	// Path is the executable in Dir.
	Path string `json:"path"`
	// Dir is the PATH directory it was found in, with symbolic links
	// resolved: a version manager's per-shell directory (fnm's
	// fnm_multishells) is gone once that shell exits.
	Dir     string `json:"dir"`
	Version string `json:"version,omitempty"`
}

// Recording turns what Find learned into a record entry; ok is false when the
// agent was not found.
func Recording(found Found) (Recorded, bool) {
	if found.Path == "" {
		return Recorded{}, false
	}
	dir := filepath.Dir(found.Path)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return Recorded{Agent: found.Agent, Path: filepath.Join(dir, filepath.Base(found.Path)), Dir: dir, Version: found.Version}, true
}

// RecordFile is the record's location: terminal.json beside config.toml.
func RecordFile() (string, error) {
	path, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "terminal.json"), nil
}

// Load reads the record at path; a missing file is an empty record.
func Load(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, nil
	}
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return record, nil
}

// Save replaces the record at path, readable only by the user.
func Save(path string, record Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".terminal-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Dirs are the record's directories that still exist, in order, once each.
func (r Record) Dirs() []string {
	var dirs []string
	for _, agent := range r.Agents {
		if !filepath.IsAbs(agent.Dir) || slices.Contains(dirs, agent.Dir) {
			continue
		}
		if info, err := os.Stat(agent.Dir); err == nil && info.IsDir() {
			dirs = append(dirs, agent.Dir)
		}
	}
	return dirs
}

// Holds reports whether the executable at path is in a recorded directory.
func (r Record) Holds(path string) bool {
	dir := filepath.Dir(path)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return slices.ContainsFunc(r.Agents, func(a Recorded) bool { return a.Dir == dir })
}

// WithDirs is the PATH value path with dirs first and their other entries
// removed.
func WithDirs(path string, dirs []string) string {
	if len(dirs) == 0 {
		return path
	}
	entries := slices.Clone(dirs)
	for _, entry := range filepath.SplitList(path) {
		if entry != "" && !slices.Contains(entries, entry) {
			entries = append(entries, entry)
		}
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

// WithRecorded is the PATH value path with the recorded directories first.
// Without a readable record it is path.
func WithRecorded(path string) string {
	file, err := RecordFile()
	if err != nil {
		return path
	}
	record, err := Load(file)
	if err != nil {
		return path
	}
	return WithDirs(path, record.Dirs())
}

// Keep sets this process's PATH to base with the recorded directories first,
// now and whenever the record changes, until ctx ends: the daemon keeps
// running while the user runs `baum desktop-setup`. changed is called after
// each change the record brings.
func Keep(ctx context.Context, base string, every time.Duration, changed func(path string)) {
	current := WithRecorded(base)
	_ = os.Setenv("PATH", current)
	file, err := RecordFile()
	if err != nil {
		return
	}
	stamp := func() time.Time {
		info, err := os.Stat(file)
		if err != nil {
			return time.Time{}
		}
		return info.ModTime()
	}
	seen := stamp()
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if now := stamp(); !now.Equal(seen) {
				seen = now
				if path := WithRecorded(base); path != current {
					current = path
					_ = os.Setenv("PATH", path)
					changed(path)
				}
			}
		}
	}()
}
