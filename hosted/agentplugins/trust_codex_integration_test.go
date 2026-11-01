//go:build unix

package agentplugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt in with the release CLI mounted into Docker. This catches packaging
// accepted by the installer but ignored by the runtime; a JSON-shape test cannot.
func TestShippedCodexPluginHooksWithoutProjectHooks(t *testing.T) {
	binary := os.Getenv("BAUM_CODEX_COMPAT_BINARY")
	if binary == "" {
		t.Skip("set BAUM_CODEX_COMPAT_BINARY to run against an installed Codex CLI")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	market := filepath.Join(root, "market")
	plugin := filepath.Join(market, "plugins", "baum")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(plugin, os.DirFS(filepath.Join("..", "..", "plugins", "baum-codex"))); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(market, ".agents", "plugins")
	if err := os.MkdirAll(catalog, 0o700); err != nil {
		t.Fatal(err)
	}
	const manifest = `{"name":"baum","plugins":[{"name":"baum","source":{"source":"local","path":"./plugins/baum"},"policy":{"installation":"AVAILABLE","authentication":"ON_INSTALL"},"category":"Productivity"}]}`
	if err := os.WriteFile(filepath.Join(catalog, "marketplace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, args := range [][]string{
		{"plugin", "marketplace", "add", market},
		{"plugin", "add", Plugin, "--json"},
	} {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native install: %v %s", err, out)
		}
	}
	client := New()
	status, err := client.Status(ctx, "codex")
	if err != nil || !status.Installed || !status.Enabled || status.Version == "local" || status.Version == "" {
		t.Fatalf("status=%+v %v", status, err)
	}
	data, err := codexHookInventory(ctx, []string{home})
	if err != nil {
		t.Fatal(err)
	}
	if got := parseHookTrust(data); got.State != "untrusted" {
		t.Fatalf("shipped plugin not loaded: %+v", got)
	}
	var inventory struct {
		Data []struct {
			Hooks []struct {
				PluginID string `json:"pluginId"`
			}
		}
	}
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Data) != 1 || len(inventory.Data[0].Hooks) != len(codexEvents) {
		t.Fatalf("wrong hook count: %s", data)
	}
	for _, hook := range inventory.Data[0].Hooks {
		if hook.PluginID != Plugin {
			t.Fatalf("non-plugin hook supplied the test: %+v", hook)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Fatal("a user hook file could mask the plugin")
	}
}
