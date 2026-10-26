package agentplugins

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// These shapes were captured from successful native installs in disposable
// Docker homes. Claude lists scopes; Codex wraps installed entries separately
// from available entries. Neither format contains hook trust evidence.
const claudeInstalled = `[{"id":"baum@baum","version":"1.0.0","scope":"user","enabled":true,"installPath":"/plugins/baum","projectEnabled":false}]`
const codexInstalled = `{"installed":[{"pluginId":"baum@baum","name":"baum","marketplaceName":"baum","version":"1.0.0","installed":true,"enabled":true,"source":{"source":"local","path":"/plugins/baum"},"installPolicy":"AVAILABLE","authPolicy":"ON_INSTALL"}],"available":[]}`

// Captured from codex-cli 0.164.0 `plugin marketplace list --json`.
const codexMarketplaces = `{"marketplaces":[{"name":"openai-bundled","root":"/codex/.tmp/bundled-marketplaces/openai-bundled","marketplaceSource":{"sourceType":"local","source":"/codex/.tmp/bundled-marketplaces/openai-bundled"}},{"name":"baum","root":"/codex/.tmp/marketplaces/baum","marketplaceSource":{"sourceType":"git","source":"https://github.com/baum-ai/baum.git"}},{"name":"openai-curated","root":"/codex/.tmp/plugins"}]}`

func TestCodexStatusSkipsListWithoutBaumMarketplace(t *testing.T) {
	for _, tc := range []struct {
		name, marketplaces string
		fails              bool
	}{
		{"not configured", `{"marketplaces":[{"name":"openai-curated","root":"/codex/.tmp/plugins"}]}`, false},
		{"missing registry", `{}`, true},
		{"invalid JSON", `not JSON`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[1] != "marketplace" {
					t.Fatalf("listed plugins without baum's marketplace: %v", args)
				}
				return []byte(tc.marketplaces), nil
			}}
			got, err := client.Status(context.Background(), "codex")
			if (err != nil) != tc.fails || got != (Status{}) {
				t.Fatalf("got %+v %v fails=%v", got, err, tc.fails)
			}
		})
	}
}

func TestNativeStatusShapes(t *testing.T) {
	for _, tc := range []struct {
		name, agent, input string
		want               Status
		fails              bool
	}{
		{"Claude user", "claude-code", claudeInstalled, Status{true, true, "1.0.0"}, false},
		{"Codex installed", "codex", codexInstalled, Status{true, true, "1.0.0"}, false},
		{"Claude project only", "claude-code", strings.Replace(claudeInstalled, `"user"`, `"project"`, 1), Status{}, false},
		{"disabled", "codex", strings.Replace(codexInstalled, `"enabled":true`, `"enabled":false`, 1), Status{true, false, "1.0.0"}, false},
		{"other marketplace", "codex", strings.ReplaceAll(codexInstalled, "baum@baum", "baum@other"), Status{}, false},
		{"available is not installed", "codex", `{"installed":[],"available":[{"pluginId":"baum@baum","installed":false}]}`, Status{}, false},
		{"empty Claude", "claude-code", `[]`, Status{}, false},
		{"missing Codex registry", "codex", `{}`, Status{}, true},
		{"Claude null", "claude-code", `null`, Status{}, true},
		{"invalid JSON", "codex", `not JSON`, Status{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStatus(tc.agent, []byte(tc.input))
			if (err != nil) != tc.fails || got != tc.want {
				t.Fatalf("got %+v %v want %+v fails=%v", got, err, tc.want, tc.fails)
			}
		})
	}
}

func TestNativePluginLifecycleCommandsAndVerification(t *testing.T) {
	for _, agent := range Agents() {
		t.Run(agent, func(t *testing.T) {
			installed := false
			var calls [][]string
			client := &Client{run: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("native command has no timeout")
				}
				if binary != Binary(agent) {
					t.Fatalf("binary=%q", binary)
				}
				calls = append(calls, append([]string(nil), args...))
				if args[1] == "marketplace" && args[2] == "list" {
					return []byte(codexMarketplaces), nil
				}
				switch args[1] {
				case "install", "add":
					installed = true
				case "remove", "uninstall":
					installed = false
				case "list":
					if agent == "codex" && !reflect.DeepEqual(args, []string{"plugin", "list", "--json", "--marketplace", "baum"}) {
						t.Fatalf("unscoped codex list %v", args)
					}
					if installed {
						if agent == "codex" {
							return []byte(codexInstalled), nil
						}
						return []byte(claudeInstalled), nil
					}
					if agent == "codex" {
						return []byte(`{"installed":[],"available":[]}`), nil
					}
					return []byte(`[]`), nil
				}
				return []byte(`{}`), nil
			}}
			for range 2 {
				status, err := client.Install(context.Background(), agent)
				if err != nil || !status.Installed || !status.Enabled {
					t.Fatalf("install=%+v %v", status, err)
				}
			}
			install := []string{"plugin", "add", Plugin, "--json"}
			remove := []string{"plugin", "remove", Plugin, "--json"}
			if agent == "claude-code" {
				install = []string{"plugin", "install", Plugin, "--scope", "user", "--json"}
				remove = []string{"plugin", "uninstall", Plugin, "--scope", "user", "--json"}
			}
			if !reflect.DeepEqual(calls[0], []string{"plugin", "marketplace", "add", Marketplace}) || !reflect.DeepEqual(calls[1], install) {
				t.Fatalf("install calls=%v", calls)
			}
			uninstallFrom := len(calls)
			if _, err := client.Uninstall(context.Background(), agent); err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(calls[uninstallFrom:], func(c []string) bool { return reflect.DeepEqual(c, remove) }) {
				t.Fatalf("remove calls=%v", calls)
			}
			before := len(calls)
			if _, err := client.Uninstall(context.Background(), agent); err != nil {
				t.Fatal(err)
			}
			for _, call := range calls[before:] {
				if call[1] == "remove" || call[1] == "uninstall" {
					t.Fatalf("absent plugin caused a mutation: %v", call)
				}
			}
		})
	}
}

func TestNativeErrorsCancellationAndUnverifiedInstall(t *testing.T) {
	failed := errors.New("old CLI has no plugin add command")
	client := &Client{run: func(context.Context, string, ...string) ([]byte, error) { return nil, failed }}
	if _, err := client.Install(context.Background(), "codex"); !errors.Is(err, failed) || !strings.Contains(err.Error(), "install/update codex") {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runCommand(ctx, "missing-coding-agent", "plugin", "list", "--json"); err == nil {
		t.Fatal("missing executable succeeded")
	}
	client.run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	if _, err := client.Status(ctx, "codex"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	client.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "marketplace" && args[2] == "list" {
			return []byte(codexMarketplaces), nil
		}
		if args[1] == "list" {
			return []byte(`{"installed":[],"available":[]}`), nil
		}
		return []byte(`{}`), nil
	}
	if _, err := client.Install(context.Background(), "codex"); err == nil {
		t.Fatal("claimed an unverified install")
	}
}
