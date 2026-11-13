// Package agentdirs knows the configuration directories of the coding agents
// in use on this computer: Claude Code's and Codex's.
//
// Claude Code keeps its settings, plugins, sign-in and session transcripts in
// one configuration directory: ~/.claude, or the directory CLAUDE_CONFIG_DIR
// names. Codex keeps the same in its home: ~/.codex, or the directory
// CODEX_HOME names. A person with two accounts runs the agent with two
// directories, so baum reads sessions from each one it knows of (All): the
// default, the directory of its own variable, and the directories of the
// agent's record.
//
// The record (RecordFile) holds the other directories baum was told about:
// `baum hooks install --user` adds the one it installs into, an agent hook
// adds the one its session runs with, and the app adds the one chosen for
// reviews, fixes or data extraction. It is a file in the user's own
// configuration folder, written only by the user's own baum processes;
// nothing a request to the daemon carries is added to it.
package agentdirs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/config"
)

// Agent describes where one coding agent keeps its configuration directory.
type Agent struct {
	// Env is the variable the agent reads its directory from.
	Env string
	// DefaultName is the directory under the home the agent uses when Env
	// is not set.
	DefaultName string
	// RecordName is the file of the agent's record, beside config.toml.
	RecordName string
	// Product is the agent's name, and What the directory's, in messages.
	Product, What string
	// signIn tells how to sign in with dir, for a refused choice.
	signIn func(dir string) string
}

// Claude is Claude Code: CLAUDE_CONFIG_DIR, else ~/.claude.
var Claude = Agent{
	Env: "CLAUDE_CONFIG_DIR", DefaultName: ".claude", RecordName: "claude-dirs.json",
	Product: "Claude Code", What: "Claude Code configuration directory",
	signIn: func(dir string) string {
		return fmt.Sprintf("Run `CLAUDE_CONFIG_DIR=%s claude` in a terminal, sign in with /login", dir)
	},
}

// Codex is Codex: CODEX_HOME, else ~/.codex.
var Codex = Agent{
	Env: "CODEX_HOME", DefaultName: ".codex", RecordName: "codex-dirs.json",
	Product: "Codex", What: "Codex home",
	signIn: func(dir string) string {
		return fmt.Sprintf("Run `CODEX_HOME=%s codex login` in a terminal", dir)
	},
}

// MaxRecorded bounds how many directories a record keeps. Remember drops the
// oldest beyond it.
const MaxRecorded = 16

// Default is the directory the agent uses when its variable is not set:
// DefaultName in home, which when empty is the user's home.
func (a Agent) Default(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, a.DefaultName)
}

// Normalize is dir as baum keeps it: a leading ~ expanded to the user's home,
// absolute and cleaned. A relative path is an error, since it would mean a
// different directory to each process that read it.
func Normalize(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		dir = filepath.Join(home, dir[1:])
	}
	if strings.ContainsRune(dir, 0) || len(dir) > 4<<10 {
		return "", errors.New("the directory must be at most 4 KiB, without NUL bytes")
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%q is not an absolute path; write the full path of the directory, or start it with ~/", dir)
	}
	return filepath.Clean(dir), nil
}

// FromEnv is the directory the agent's variable names for this process, or
// "" when it is unset or not an absolute path.
func (a Agent) FromEnv() string {
	dir, err := Normalize(os.Getenv(a.Env))
	if err != nil {
		return ""
	}
	return dir
}

// Current is the directory the agent would use if this process started it:
// FromEnv, else Default.
func (a Agent) Current() string {
	if dir := a.FromEnv(); dir != "" {
		return dir
	}
	return a.Default("")
}

// RecordFile is the record's location: RecordName beside config.toml.
func (a Agent) RecordFile() (string, error) {
	path, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), a.RecordName), nil
}

type record struct {
	Dirs []string `json:"dirs"`
}

// Load reads the record at path: its absolute directories, once each. A
// missing file is an empty record.
func Load(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var dirs []string
	for _, dir := range r.Dirs {
		if filepath.IsAbs(dir) && !slices.Contains(dirs, filepath.Clean(dir)) {
			dirs = append(dirs, filepath.Clean(dir))
		}
	}
	return dirs, nil
}

func save(path string, dirs []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record{Dirs: dirs}, "", "  ")
	if err != nil {
		return err
	}
	pattern := "." + strings.TrimSuffix(filepath.Base(path), ".json") + "-*.json"
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
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

// Recorded are the directories of the user's record. A record that cannot be
// read is empty.
func (a Agent) Recorded() []string {
	path, err := a.RecordFile()
	if err != nil {
		return nil
	}
	dirs, _ := Load(path)
	return dirs
}

// Remember adds dir to the user's record and reports whether it changed it.
// The default directory is never recorded, since it is always read; neither
// is a path that is not an existing directory.
func (a Agent) Remember(dir string) (bool, error) {
	path, err := a.RecordFile()
	if err != nil {
		return false, err
	}
	return a.RememberIn(path, dir)
}

// RememberIn is Remember for the record at path.
func (a Agent) RememberIn(path, dir string) (bool, error) {
	dir, err := Normalize(dir)
	if err != nil {
		return false, err
	}
	if dir == a.Default("") {
		return false, nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	dirs, err := Load(path)
	if err != nil {
		// A record that cannot be read is replaced rather than left to
		// block every later one.
		dirs = nil
	}
	if slices.Contains(dirs, dir) {
		return false, nil
	}
	dirs = append(dirs, dir)
	if len(dirs) > MaxRecorded {
		dirs = dirs[len(dirs)-MaxRecorded:]
	}
	return true, save(path, dirs)
}

// Forget removes dir from the user's record and reports whether it was there.
func (a Agent) Forget(dir string) (bool, error) {
	path, err := a.RecordFile()
	if err != nil {
		return false, err
	}
	return ForgetIn(path, dir)
}

// ForgetIn is Forget for the record at path.
func ForgetIn(path, dir string) (bool, error) {
	dir, err := Normalize(dir)
	if err != nil {
		return false, err
	}
	dirs, err := Load(path)
	if err != nil {
		return false, err
	}
	kept := slices.DeleteFunc(slices.Clone(dirs), func(d string) bool { return d == dir })
	if len(kept) == len(dirs) {
		return false, nil
	}
	return true, save(path, kept)
}

// All are the directories baum reads the agent's files from, once each: the
// default in home (the user's when empty), then the directory of this
// process's variable, then the record's. They need not exist.
func (a Agent) All(home string) []string {
	return merge(a.Default(home), a.FromEnv(), a.Recorded())
}

func merge(def, env string, recorded []string) []string {
	var dirs []string
	for _, dir := range append([]string{def, env}, recorded...) {
		if dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// Under is name inside each of All(home): Claude.Under(home, "projects") are
// Claude Code's session stores, Codex.Under(home, "sessions") Codex's.
func (a Agent) Under(home, name string) []string {
	return Under(a.All(home), name)
}

// Under is name inside each of dirs.
func Under(dirs []string, name string) []string {
	out := make([]string, len(dirs))
	for i, dir := range dirs {
		out[i] = filepath.Join(dir, name)
	}
	return out
}
