package userhooks

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

// PluginManaged reports an explicit migration to agent-managed plugins.
func PluginManaged(home, agent string) bool {
	return isRegular(filepath.Join(stateDir(home), "user-hooks", agent+".plugin"))
}

// Migrate removes only baum's legacy dispatchers, then remembers the plugin
// choice so later setup and desktop installs cannot restore duplicate hooks.
// A failed marker write is retryable; plugins also yield to legacy handlers.
func Migrate(home, agent string) (Result, error) {
	result, err := Uninstall(home, agent)
	if err != nil {
		return result, err
	}
	path := filepath.Join(stateDir(home), "user-hooks", agent+".plugin")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return result, err
	}
	return result, writeAtomic(path, []byte("1\n"), 0o600, nil)
}

// RestoreLegacy installs settings-based hooks before forgetting the plugin
// choice. The caller must first disable or uninstall the agent plugin. If
// installation fails, the choice is retained. If marker removal fails, the
// installed hooks are safe (plugins yield to them) and retrying is idempotent.
func RestoreLegacy(home, agent string) (Result, error) {
	return restoreLegacy(home, agent, os.Remove)
}

func restoreLegacy(home, agent string, removeMarker func(string) error) (Result, error) {
	result, err := update(home, agent, true)
	if err != nil {
		return result, err
	}
	err = removeMarker(filepath.Join(stateDir(home), "user-hooks", agent+".plugin"))
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return result, fmt.Errorf("legacy hooks installed; could not clear plugin choice (retry install --user --legacy): %w", err)
	}
	return result, nil
}

// HasLegacyEvent reports whether a legacy baum handler already covers event.
// Unreadable settings are refused rather than risking duplicate delivery.
func HasLegacyEvent(home, agent, event string) (bool, error) {
	settings, ok := agenthooks.LookupUserWide(agent)
	if !ok {
		return false, fmt.Errorf("unsupported coding agent %q", agent)
	}
	file, err := readSettingsFile(filepath.Join(home, filepath.FromSlash(settings.SettingsFile)))
	if err != nil {
		return false, err
	}
	if !file.existed {
		return false, nil
	}
	parsed, err := parseSettings(file.contents)
	if err != nil {
		return false, err
	}
	for _, owned := range parsed.owned {
		if owned.agent == agent && owned.event == event {
			return true, nil
		}
	}
	return false, nil
}
