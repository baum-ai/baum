package agentplugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var codexEvents = []string{"sessionStart", "userPromptSubmit", "postToolUse", "stop", "sessionEnd", "subagentStart", "subagentStop", "permissionRequest"}

func hookInventory(status string) map[string]any {
	hooks := []any{}
	for _, event := range codexEvents {
		hooks = append(hooks, map[string]any{"pluginId": Plugin, "eventName": event, "enabled": true, "trustStatus": status})
	}
	return map[string]any{"data": []any{map[string]any{"hooks": hooks, "errors": []any{}}}}
}

func TestHookTrustRequiresEveryBaumHookToBePresentEnabledAndTrusted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{"trusted", func(map[string]any) {}, "trusted"},
		{"untrusted", func(d map[string]any) { hookRows(d)[0].(map[string]any)["trustStatus"] = "untrusted" }, "untrusted"},
		{"modified", func(d map[string]any) { hookRows(d)[0].(map[string]any)["trustStatus"] = "modified" }, "untrusted"},
		{"disabled", func(d map[string]any) { hookRows(d)[0].(map[string]any)["enabled"] = false }, "disabled"},
		{"missing event", func(d map[string]any) { scope(d)["hooks"] = hookRows(d)[1:] }, "missing"},
		{"empty hooks", func(d map[string]any) { scope(d)["hooks"] = []any{} }, "missing"},
		{"other plugins do not count", func(d map[string]any) { hookRows(d)[0].(map[string]any)["pluginId"] = "baum@other" }, "missing"},
		{"unrelated untrusted hook", func(d map[string]any) {
			scope(d)["hooks"] = append(hookRows(d), map[string]any{"pluginId": "other@other", "enabled": true, "trustStatus": "untrusted"})
		}, "trusted"},
		{"unknown trust state", func(d map[string]any) { hookRows(d)[0].(map[string]any)["trustStatus"] = "future" }, "unknown"},
		{"enabled not reported", func(d map[string]any) { delete(hookRows(d)[0].(map[string]any), "enabled") }, "unknown"},
		{"bad registry", func(d map[string]any) { delete(scope(d), "hooks") }, "unknown"},
		{"error", func(d map[string]any) { scope(d)["errors"] = []any{"private config content"} }, "unknown"},
		{"no scopes", func(d map[string]any) { d["data"] = []any{} }, "unknown"},
		{"multiple repositories", func(d map[string]any) {
			other := hookInventory("untrusted")
			d["data"] = append(d["data"].([]any), scope(other))
		}, "untrusted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := hookInventory("trusted")
			tc.change(d)
			data, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if got := parseHookTrust(data); got.State != tc.want {
				t.Fatalf("got %+v want %s", got, tc.want)
			}
		})
	}
	for _, data := range []string{"", "{}", "null", "bad JSON"} {
		if got := parseHookTrust([]byte(data)); got.State != "unknown" {
			t.Fatalf("%q: %+v", data, got)
		}
	}
}

func scope(data map[string]any) map[string]any { return data["data"].([]any)[0].(map[string]any) }
func hookRows(data map[string]any) []any       { return scope(data)["hooks"].([]any) }

func TestHookTrustDefaultsToUserHomeAndBoundsTheQuery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	client := &Client{hooks: func(ctx context.Context, dirs []string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		if len(dirs) != 1 || dirs[0] != home {
			t.Fatalf("directories=%v", dirs)
		}
		return json.Marshal(hookInventory("trusted"))
	}}
	if got := client.HookTrust(context.Background(), nil); got.State != "trusted" {
		t.Fatalf("%+v", got)
	}
	client.hooks = func(context.Context, []string) ([]byte, error) { return nil, errors.New("secret output") }
	if got := client.HookTrust(context.Background(), nil); got != (HookTrust{State: "unknown"}) {
		t.Fatalf("query failure leaked: %+v", got)
	}
}

func TestHookTrustDoesNotReadOrChangeTrustFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, "config.toml")
	const body = "[hooks.state]\nprivate = 'unchanged'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{hooks: func(context.Context, []string) ([]byte, error) { return json.Marshal(hookInventory("untrusted")) }}
	if got := client.HookTrust(context.Background(), nil); got.State != "untrusted" {
		t.Fatal(got)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) != strings.TrimSpace(body) {
		t.Fatalf("changed config: %q %v", data, err)
	}
}
