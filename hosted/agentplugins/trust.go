package agentplugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/internal/proc"
)

// HookTrust reports readiness of baum's current Codex hook definitions. Unknown
// is distinct from untrusted, including on Codex versions without hooks/list.
type HookTrust struct {
	State string `json:"state"`
}

// CodexTrust checks installation before asking Codex to evaluate hook trust.
// It never starts a conversation or grants trust.
func (c *Client) CodexTrust(ctx context.Context, directories []string) HookTrust {
	if _, err := exec.LookPath("codex"); err != nil {
		return HookTrust{State: "not_installed"}
	}
	status, err := c.Status(ctx, "codex")
	if err != nil {
		return HookTrust{State: "unknown"}
	}
	if !status.Installed {
		return HookTrust{State: "not_installed"}
	}
	if !status.Enabled {
		return HookTrust{State: "disabled"}
	}
	return c.HookTrust(ctx, directories)
}

// HookTrust inspects an installed, enabled plugin in the supplied directories.
// The user's home is the default, excluding the caller's project overrides.
func (c *Client) HookTrust(ctx context.Context, directories []string) HookTrust {
	ctx, cancel := context.WithTimeout(ctx, StatusTimeout)
	defer cancel()
	if len(directories) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return HookTrust{State: "unknown"}
		}
		directories = []string{home}
	}
	query := c.hooks
	if query == nil {
		query = codexHookInventory
	}
	data, err := query(ctx, directories)
	if err != nil {
		return HookTrust{State: "unknown"}
	}
	return parseHookTrust(data)
}

func parseHookTrust(data []byte) HookTrust {
	var inventory struct {
		Disabled bool
		Data     *[]struct {
			Hooks *[]struct {
				PluginID    string `json:"pluginId"`
				Event       string `json:"eventName"`
				Enabled     *bool
				TrustStatus string `json:"trustStatus"`
			}
			Errors []json.RawMessage
		}
	}
	unknown := HookTrust{State: "unknown"}
	if json.Unmarshal(data, &inventory) != nil {
		return unknown
	}
	if inventory.Disabled {
		return HookTrust{State: "hooks_disabled"}
	}
	if inventory.Data == nil || len(*inventory.Data) == 0 {
		return unknown
	}
	state := "trusted"
	spec, _ := agenthooks.LookupUserWide("codex")
	for _, scope := range *inventory.Data {
		if scope.Hooks == nil || len(scope.Errors) != 0 {
			return unknown
		}
		seen := map[string]bool{}
		for _, hook := range *scope.Hooks {
			if hook.PluginID != Plugin {
				continue
			}
			seen[hook.Event] = true
			if hook.Enabled == nil {
				return unknown
			}
			if !*hook.Enabled {
				state = "disabled"
			}
			switch hook.TrustStatus {
			case "trusted", "managed":
			case "untrusted", "modified":
				if state != "disabled" {
					state = "untrusted"
				}
			default:
				return unknown
			}
		}
		for _, placement := range spec.Placements {
			event := strings.ToLower(placement.HookEvent[:1]) + placement.HookEvent[1:]
			if !seen[event] {
				return HookTrust{State: "missing"}
			}
		}
	}
	return HookTrust{State: state}
}

// codexHookInventory uses a private stdio server solely to read effective
// configuration. It creates no thread, executes no hook and never requests a
// model turn. Errors and stderr are deliberately not returned: they may expose
// the user's configuration or other plugins' commands.
func codexHookInventory(ctx context.Context, directories []string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://")
	proc.Configure(cmd)
	cmd.WaitDelay = time.Second
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cmd.Dir = home
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return nil, err
	}
	defer func() {
		cancel()
		_ = in.Close()
		_ = cmd.Wait()
	}()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	encode := json.NewEncoder(in)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if err := encode.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for scanner.Scan() {
			var response struct {
				ID     *int
				Result json.RawMessage
				Error  json.RawMessage
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil {
				return nil, errors.New("invalid Codex response")
			}
			if response.ID == nil || *response.ID != id {
				continue
			}
			if len(response.Error) > 0 && string(response.Error) != "null" {
				return nil, errors.New("Codex hook status is unavailable")
			}
			return response.Result, nil
		}
		return nil, errors.New("Codex closed the hook status connection")
	}
	if _, err := call(1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "baum_hook_status", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}); err != nil {
		return nil, err
	}
	if err := encode.Encode(map[string]string{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		return nil, err
	}
	data, err := call(2, "config/read", map[string]bool{"includeLayers": false})
	if err != nil {
		return nil, err
	}
	var config struct {
		Config struct {
			Features struct {
				Hooks       *bool
				LegacyHooks *bool `json:"codex_hooks"`
			}
		}
	}
	if json.Unmarshal(data, &config) != nil {
		return nil, errors.New("invalid Codex config response")
	}
	hooks := config.Config.Features.Hooks
	if hooks == nil {
		hooks = config.Config.Features.LegacyHooks
	}
	if hooks != nil && !*hooks {
		return []byte(`{"disabled":true}`), nil
	}
	return call(3, "hooks/list", map[string]any{"cwds": slices.Clone(directories)})
}
