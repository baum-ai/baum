package agentplugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeHooksReadsTheSettingsThatStopPluginHooks(t *testing.T) {
	for _, c := range []struct {
		name    string
		user    string
		managed map[string]string
		want    string
	}{
		{name: "no settings", want: "ready"},
		{name: "other keys", user: `{"model":"opus","disableAllHooks":false}`, want: "ready"},
		{name: "user turns hooks off", user: `{"disableAllHooks":true}`, want: "hooks_disabled"},
		{name: "managed turns hooks off", managed: map[string]string{"managed-settings.json": `{"disableAllHooks":true}`}, want: "hooks_disabled"},
		{name: "managed overrides user", user: `{"disableAllHooks":true}`, managed: map[string]string{"managed-settings.json": `{"disableAllHooks":false}`}, want: "ready"},
		{name: "managed hooks only", managed: map[string]string{"managed-settings.json": `{"allowManagedHooksOnly":true}`}, want: "managed_only"},
		{name: "baum force-enabled", managed: map[string]string{"managed-settings.json": `{"allowManagedHooksOnly":true,"enabledPlugins":{"baum@baum":true}}`}, want: "ready"},
		{name: "drop-in wins", managed: map[string]string{
			"managed-settings.json":            `{"allowManagedHooksOnly":true}`,
			"managed-settings.d/10-hooks.json": `{"allowManagedHooksOnly":false}`,
			"managed-settings.d/.hidden.json":  `{"disableAllHooks":true}`,
			"managed-settings.d/20-notes.txt":  `{"disableAllHooks":true}`,
		}, want: "ready"},
		{name: "unreadable user file", user: `{`, want: "unknown"},
		{name: "unreadable managed file", managed: map[string]string{"managed-settings.json": `{`}, want: "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			user := filepath.Join(dir, "settings.json")
			if c.user != "" {
				writeSettings(t, user, c.user)
			}
			managed := filepath.Join(dir, "managed")
			for name, body := range c.managed {
				writeSettings(t, filepath.Join(managed, name), body)
			}
			if got := claudeHooks(user, managed).State; got != c.want {
				t.Fatalf("state = %q, want %q", got, c.want)
			}
		})
	}
}

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
