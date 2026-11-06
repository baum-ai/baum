package agentplugins

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Claude Code has no per-hook approval: an enabled plugin's hooks run in any
// folder the user trusted. What can still stop them is settings.
// ClaudeHooks reads the user's settings and the file-based managed settings
// for those keys. It reports:
//   - hooks_disabled: disableAllHooks is on, in managed or user settings
//   - managed_only: managed allowManagedHooksOnly blocks plugin hooks, and
//     managed enabledPlugins does not force-enable baum
//   - unknown: a settings file exists but cannot be read
//   - ready: nothing these files set stops baum's hooks
//
// Managed settings delivered by MDM or the claude.ai console are not files
// here, so ready cannot rule out a policy from those.
func ClaudeHooks() HookTrust {
	home, err := os.UserHomeDir()
	if err != nil {
		return HookTrust{State: "unknown"}
	}
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		config = filepath.Join(home, ".claude")
	}
	return claudeHooks(filepath.Join(config, "settings.json"), claudeManagedDirectory())
}

func claudeManagedDirectory() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

type claudeSettings struct {
	DisableAllHooks       *bool           `json:"disableAllHooks"`
	AllowManagedHooksOnly *bool           `json:"allowManagedHooksOnly"`
	EnabledPlugins        map[string]bool `json:"enabledPlugins"`
}

func claudeHooks(userFile, managedDir string) HookTrust {
	managed, err := readClaudeManaged(managedDir)
	if err != nil {
		return HookTrust{State: "unknown"}
	}
	user, err := readClaudeSettings(userFile)
	if err != nil {
		return HookTrust{State: "unknown"}
	}
	// A managed value wins over the user's; either way true turns hooks off.
	disabled := managed.DisableAllHooks
	if disabled == nil {
		disabled = user.DisableAllHooks
	}
	if disabled != nil && *disabled {
		return HookTrust{State: "hooks_disabled"}
	}
	if managed.AllowManagedHooksOnly != nil && *managed.AllowManagedHooksOnly && !managed.EnabledPlugins[Plugin] {
		return HookTrust{State: "managed_only"}
	}
	return HookTrust{State: "ready"}
}

// readClaudeManaged merges managed-settings.json, then each visible *.json in
// managed-settings.d in name order, as Claude Code does: later files win.
func readClaudeManaged(dir string) (claudeSettings, error) {
	files := []string{filepath.Join(dir, "managed-settings.json")}
	entries, err := os.ReadDir(filepath.Join(dir, "managed-settings.d"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return claudeSettings{}, err
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		files = append(files, filepath.Join(dir, "managed-settings.d", name))
	}
	merged := claudeSettings{EnabledPlugins: map[string]bool{}}
	for _, file := range files {
		s, err := readClaudeSettings(file)
		if err != nil {
			return claudeSettings{}, err
		}
		if s.DisableAllHooks != nil {
			merged.DisableAllHooks = s.DisableAllHooks
		}
		if s.AllowManagedHooksOnly != nil {
			merged.AllowManagedHooksOnly = s.AllowManagedHooksOnly
		}
		maps.Copy(merged.EnabledPlugins, s.EnabledPlugins)
	}
	return merged, nil
}

// readClaudeSettings reads the keys that gate hooks; a missing file sets none.
func readClaudeSettings(file string) (claudeSettings, error) {
	var s claudeSettings
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}
