package baumcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

// The refusals of `baum hooks agent --check --user`. Each one leaves the
// user-wide hook doing nothing, which is what it does without baum.
var (
	errUserHookOutsideRepository = errors.New("not in a Git repository")
	errUserHookNoEntire          = errors.New("Entire is not enabled in this repository")
	errUserHookHandledElsewhere  = errors.New("another hook the agent runs already hands this event to Entire")
)

// Where agents read hooks that neither the user nor the repository wrote.
// Tests point them elsewhere.
var (
	claudeManagedDir = defaultClaudeManagedDir()
	codexSystemDir   = "/etc/codex"
)

func defaultClaudeManagedDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

// userHooksEnabled answers `baum hooks agent --check --user`, the gate of the
// marketplace hooks `baum hooks install --user` installs through the agent,
// which the agent runs in every directory. They run baum only where the
// committed hooks would and only where nothing else already hands the event
// to Entire:
//
//   - inside a Git repository;
//   - whose Entire is enabled, by Entire's own rules, or, failing that, for a
//     user who runs the baum daemon: the desktop app tracks live sessions from
//     the hook events alone, so there the hook only records the event (see
//     userHookRunsEntire) and installs nothing in the repository;
//   - where no other hook the agent runs for the event calls Entire (see
//     handledElsewhere), so the event reaches Entire once, not twice;
//   - and where the committed dispatcher's `--check` would succeed
//     (hooksEnabled), so the opt-in rules are the same.
//
// Without a daemon, a repository without Entire is refused: the hook's only
// work there, delegating to Entire, would do nothing, and the session-start
// bootstrap would install Git hooks in a repository that never asked for
// them. The checks read a few local files and start no process, so the hook
// costs a repository that does not use Entire almost nothing.
func userHooksEnabled(ctx context.Context, dir, agent, event string) error {
	spec, _ := lookupAgentHookSpec(agent)
	settings, ok := agenthooks.LookupUserWide(agent)
	if !ok {
		return fmt.Errorf("%s has no user-wide hooks", agent)
	}
	start, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, ok := enclosingWorktree(start)
	if !ok {
		return errUserHookOutsideRepository
	}
	entire := entireEnabled(root)
	if !entire && !daemonHasRun() {
		return errUserHookNoEntire
	}
	if handledElsewhere(start, root, spec, settings, event) {
		return errUserHookHandledElsewhere
	}
	if !entire {
		return nil
	}
	return hooksEnabled(ctx, dir)
}

// userHookRunsEntire reports whether a user-wide hook that passed its check
// in dir may hand its event to Entire: only where the repository enabled
// Entire. Elsewhere it passed because the user runs the daemon, so it records
// the event and does nothing else, logged in or not.
func userHookRunsEntire(dir string) bool {
	start, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	root, ok := enclosingWorktree(start)
	return ok && entireEnabled(root)
}

// handledElsewhere reports whether a hook the agent runs besides baum's
// user-wide one hands this event to Entire. The committed dispatcher and
// Entire's own command both end in `entire hooks AGENT
// EVENT`, so the files are searched for that text:
//
//   - Claude Code: the project's .claude/settings.json and
//     settings.local.json (working directory up to the root, and
//     CLAUDE_PROJECT_DIR), ~/.claude/settings.json and the managed settings
//     (managed-settings.json and managed-settings.d/*.json);
//   - Codex: ~/.codex/hooks.json and config.toml and the files in
//     /etc/codex, and the project's .codex/hooks.json and config.toml, but
//     those only where Codex would run them: in a project trusted in
//     ~/.codex/config.toml, and for a hook the user trusted there.
//
// Plugin hooks cannot be enumerated reliably and are not looked at.
func handledElsewhere(start, root string, spec agentHookSpec, settings agenthooks.UserWide, event string) bool {
	home, _ := os.UserHomeDir()
	dirs := projectDirs(start, root)
	switch spec.Slug {
	case "claude-code":
		if project := os.Getenv("CLAUDE_PROJECT_DIR"); project != "" {
			dirs = append(dirs, project)
		}
		paths := projectFiles(dirs, settings)
		if home != "" {
			paths = append(paths, filepath.Join(home, ".claude", "settings.json"))
		}
		paths = append(paths, filepath.Join(claudeManagedDir, "managed-settings.json"))
		if dropIns, err := filepath.Glob(filepath.Join(claudeManagedDir, "managed-settings.d", "*.json")); err == nil {
			paths = append(paths, dropIns...)
		}
		for _, path := range paths {
			if fileRunsEntire(path, spec, event) {
				return true
			}
		}
	case "codex":
		var paths []string
		if home != "" {
			paths = append(paths, filepath.Join(home, ".codex", "hooks.json"), filepath.Join(home, ".codex", "config.toml"))
		}
		if entries, err := os.ReadDir(codexSystemDir); err == nil {
			for _, entry := range entries {
				if ext := filepath.Ext(entry.Name()); ext == ".toml" || ext == ".json" {
					paths = append(paths, filepath.Join(codexSystemDir, entry.Name()))
				}
			}
		}
		for _, path := range paths {
			if fileRunsEntire(path, spec, event) {
				return true
			}
		}
		var config *codexConfig
		for _, path := range projectFiles(dirs, settings) {
			if !fileRunsEntire(path, spec, event) {
				continue
			}
			if config == nil {
				config = readCodexConfig(home)
			}
			if config.trustedProject(dirs) && config.trustedHook(path, spec, event) {
				return true
			}
		}
	}
	return false
}

func projectFiles(dirs []string, settings agenthooks.UserWide) []string {
	var paths []string
	for _, dir := range dirs {
		for _, file := range settings.ProjectFiles {
			paths = append(paths, filepath.Join(dir, filepath.FromSlash(file)))
		}
	}
	return paths
}

// enclosingWorktree finds the nearest directory at or above dir holding a
// .git directory or file, without starting git.
func enclosingWorktree(dir string) (string, bool) {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// entireEnabled applies Entire's own reading of its settings: a
// .entire/settings.json or settings.local.json enables it unless an
// "enabled" key says otherwise, the local file's key wins, and a file that is
// not valid JSON disables it.
func entireEnabled(root string) bool {
	found, enabled := false, false
	for _, name := range []string{"settings.json", "settings.local.json"} {
		contents, err := readSmallFile(filepath.Join(root, ".entire", name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false
		}
		var settings struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(contents, &settings); err != nil {
			return false
		}
		if !found {
			found, enabled = true, true
		}
		if settings.Enabled != nil {
			enabled = *settings.Enabled
		}
	}
	return found && enabled
}

// projectDirs lists dir and its parents up to root, where agents look for
// project settings.
func projectDirs(dir, root string) []string {
	dirs := []string{dir}
	for dir != root {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		dirs = append(dirs, dir)
	}
	return dirs
}

// errSettingsTooLarge marks a file over maxSettingsFileBytes.
var errSettingsTooLarge = errors.New("larger than the settings bound")

// readSmallFile reads a regular file of at most maxSettingsFileBytes;
// anything else that exists is an error.
func readSmallFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxSettingsFileBytes {
		return nil, errSettingsTooLarge
	}
	return os.ReadFile(path)
}

// fileRunsEntire reports whether a hook file hands the event to Entire. A file
// that exists but cannot be read in full counts as doing so: missing an event
// is better than reporting it twice.
func fileRunsEntire(path string, spec agentHookSpec, event string) bool {
	contents, err := readSmallFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	return agenthooks.HandlesEvent(contents, spec, event)
}

// codexConfig is what ~/.codex/config.toml records about trust: the projects
// whose .codex layer Codex loads, and the hooks the user reviewed, keyed by
// "<file>:<event>:<group>:<handler>".
type codexConfig struct {
	Projects map[string]struct {
		TrustLevel string `toml:"trust_level"`
	} `toml:"projects"`
	Hooks struct {
		State map[string]struct {
			TrustedHash string `toml:"trusted_hash"`
		} `toml:"state"`
	} `toml:"hooks"`
}

// readCodexConfig reads the user's Codex configuration. A missing or
// unreadable one trusts nothing, as Codex then would not either.
func readCodexConfig(home string) *codexConfig {
	config := &codexConfig{}
	if home == "" {
		return config
	}
	contents, err := readSmallFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		return config
	}
	if err := toml.Unmarshal(contents, config); err != nil {
		return &codexConfig{}
	}
	return config
}

// trustedProject reports whether Codex trusts the project, by its working
// directory or a parent up to the repository root.
func (c *codexConfig) trustedProject(dirs []string) bool {
	for _, dir := range dirs {
		for _, path := range pathForms(dir) {
			if project, ok := c.Projects[path]; ok && project.TrustLevel == "trusted" {
				return true
			}
		}
	}
	return false
}

// trustedHook reports whether the user trusted a hook in a project file that
// hands the event to Entire. Codex records the trust against a hash of the
// hook it does not document, so a hook changed since it was trusted still
// counts here.
func (c *codexConfig) trustedHook(path string, spec agentHookSpec, event string) bool {
	contents, err := readSmallFile(path)
	if err != nil {
		return false
	}
	positions, err := codexHookPositions(path, contents)
	if err != nil {
		return false
	}
	for _, position := range positions {
		if !agenthooks.HandlesEvent([]byte(position.command), spec, event) {
			continue
		}
		for _, form := range pathForms(path) {
			key := fmt.Sprintf("%s:%s:%d:%d", form, snakeCase(position.event), position.group, position.handler)
			if state, ok := c.Hooks.State[key]; ok && state.TrustedHash != "" {
				return true
			}
		}
	}
	return false
}

type codexHookPosition struct {
	event          string
	group, handler int
	command        string
}

// codexHookPositions lists the command hooks of a Codex hooks.json, or of the
// inline [hooks] tables of a config.toml, with their positions.
func codexHookPositions(path string, contents []byte) ([]codexHookPosition, error) {
	type handler struct {
		Command string `json:"command" toml:"command"`
	}
	type group struct {
		Hooks []handler `json:"hooks" toml:"hooks"`
	}
	events := map[string][]group{}
	if filepath.Ext(path) == ".toml" {
		var file struct {
			Hooks map[string]any `toml:"hooks"`
		}
		if err := toml.Unmarshal(contents, &file); err != nil {
			return nil, err
		}
		for name, value := range file.Hooks {
			if name == "state" {
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			var groups []group
			if json.Unmarshal(encoded, &groups) == nil {
				events[name] = groups
			}
		}
	} else {
		var file struct {
			Hooks map[string][]group `json:"hooks"`
		}
		if err := json.Unmarshal(contents, &file); err != nil {
			return nil, err
		}
		events = file.Hooks
	}
	var positions []codexHookPosition
	for name, groups := range events {
		for g, group := range groups {
			for h, handler := range group.Hooks {
				positions = append(positions, codexHookPosition{event: name, group: g, handler: h, command: handler.Command})
			}
		}
	}
	return positions, nil
}

// pathForms is a path as given and with symbolic links resolved, since the
// path Codex recorded may be either.
func pathForms(path string) []string {
	forms := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		forms = append(forms, resolved)
	}
	return forms
}

// snakeCase turns a hook event name into the form Codex keys trust with:
// PostToolUse becomes post_tool_use.
func snakeCase(name string) string {
	var out strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				out.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		out.WriteRune(r)
	}
	return out.String()
}
