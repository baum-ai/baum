package agenthooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Read the shipped files, not generated fixtures: the OSS subtree is the
// marketplace root after publication and must carry everything it references.
func TestMarketplacePluginsMatchUserWideHooks(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent, func(t *testing.T) {
			spec, _ := Lookup(agent)
			settings, _ := LookupUserWide(agent)
			plugin := filepath.Join(root, "plugins", "baum-"+agent)
			manifestPath := filepath.Join(plugin, "plugin.json")
			if agent == "claude-code" {
				manifestPath = filepath.Join(plugin, ".claude-plugin", "plugin.json")
			}
			var manifest map[string]any
			readPluginJSON(t, manifestPath, &manifest)
			if agent == "codex" && manifest["$schema"] != "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json" {
				t.Fatalf("Codex portable manifest must declare its format: %#v", manifest)
			}
			if manifest["name"] != "baum" || manifest["version"] != "1.0.0" || manifest["hooks"] != nil {
				t.Fatalf("manifest = %#v", manifest)
			}
			expected := map[string][]map[string]any{}
			for _, p := range settings.Placements {
				handler := map[string]any{"type": "command", "command": UserDispatcher(spec, p.Event)}
				if p.Timeout != 0 {
					handler["timeout"] = float64(p.Timeout)
				}
				group := map[string]any{"hooks": []any{handler}}
				if p.Matcher != nil {
					group["matcher"] = *p.Matcher
				}
				expected[p.HookEvent] = append(expected[p.HookEvent], group)
			}
			var hooks struct {
				Hooks map[string][]map[string]any `json:"hooks"`
			}
			readPluginJSON(t, filepath.Join(plugin, "hooks", "hooks.json"), &hooks)
			if !reflect.DeepEqual(hooks.Hooks, expected) {
				t.Fatalf("plugin hooks differ from shared event/matcher/timeout/dispatcher contract: got %#v want %#v", hooks.Hooks, expected)
			}
		})
	}
	var claude struct {
		Name    string
		Owner   map[string]any
		Plugins []struct{ Name, Source, Version string }
	}
	readPluginJSON(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), &claude)
	if claude.Name != "baum" || len(claude.Owner) == 0 || len(claude.Plugins) != 1 || claude.Plugins[0].Source != "./plugins/baum-claude-code" || claude.Plugins[0].Version != "1.0.0" {
		t.Fatalf("Claude catalog = %#v", claude)
	}
	var codex struct {
		Name    string
		Plugins []struct {
			Name     string
			Source   struct{ Source, Path string }
			Policy   struct{ Installation, Authentication string }
			Category string
		}
	}
	readPluginJSON(t, filepath.Join(root, ".agents", "plugins", "marketplace.json"), &codex)
	if codex.Name != "baum" || len(codex.Plugins) != 1 {
		t.Fatalf("Codex catalog = %#v", codex)
	}
	p := codex.Plugins[0]
	if p.Name != "baum" || p.Source.Source != "local" || p.Source.Path != "./plugins/baum-codex" || p.Policy.Installation != "AVAILABLE" || p.Policy.Authentication != "ON_INSTALL" || p.Category != "Productivity" {
		t.Fatalf("Codex plugin = %#v", p)
	}
}
func readPluginJSON(t *testing.T, path string, target any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, target); err != nil {
		t.Fatal(err)
	}
}
