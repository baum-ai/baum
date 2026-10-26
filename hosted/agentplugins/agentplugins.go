// Package agentplugins installs baum's user-scoped marketplace plugins through
// the coding agents' native CLIs. The agent owns its registry and settings.
package agentplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/proc"
)

const (
	Plugin        = "baum@baum"
	Marketplace   = "baum-ai/baum"
	StatusTimeout = 10 * time.Second
	ActionTimeout = 90 * time.Second
	// marketplaceName is the marketplace part of Plugin, as agents name it.
	marketplaceName = "baum"
)

func Agents() []string { return []string{"claude-code", "codex"} }

func Binary(agent string) string {
	switch agent {
	case "claude-code":
		return "claude"
	case "codex":
		return "codex"
	default:
		return ""
	}
}

type Status struct {
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Version   string `json:"version,omitempty"`
}

type Client struct {
	run func(context.Context, string, ...string) ([]byte, error)
}

func New() *Client { return &Client{run: runCommand} }

func runCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	proc.Configure(cmd)
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
	}
	return output, err
}

func (c *Client) command(ctx context.Context, agent string, args ...string) ([]byte, error) {
	binary := Binary(agent)
	if binary == "" {
		return nil, fmt.Errorf("unsupported coding agent %q", agent)
	}
	output, err := c.run(ctx, binary, args...)
	if err != nil {
		return nil, fmt.Errorf("%s plugin command failed: %w; install/update %s, or manage %s in its plugin UI", binary, err, binary, Plugin)
	}
	return output, nil
}

func (c *Client) Status(ctx context.Context, agent string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, StatusTimeout)
	defer cancel()
	args := []string{"plugin", "list", "--json"}
	if agent == "codex" {
		// An unscoped Codex list fetches every remote marketplace, which can
		// take 30 seconds without a cache. Listing only baum's marketplace
		// stays local, so check that it is configured and scope the list to it.
		configured, err := c.codexMarketplaceConfigured(ctx)
		if err != nil || !configured {
			return Status{}, err
		}
		args = append(args, "--marketplace", marketplaceName)
	}
	output, err := c.command(ctx, agent, args...)
	if err != nil {
		return Status{}, err
	}
	return parseStatus(agent, output)
}

func (c *Client) codexMarketplaceConfigured(ctx context.Context) (bool, error) {
	output, err := c.command(ctx, "codex", "plugin", "marketplace", "list", "--json")
	if err != nil {
		return false, err
	}
	return parseCodexMarketplaces(output)
}

func parseCodexMarketplaces(output []byte) (bool, error) {
	var list struct {
		Marketplaces *[]struct{ Name string }
	}
	if err := json.Unmarshal(output, &list); err != nil || list.Marketplaces == nil {
		return false, fmt.Errorf("codex plugin marketplace list returned invalid JSON; update Codex")
	}
	for _, m := range *list.Marketplaces {
		if m.Name == marketplaceName {
			return true, nil
		}
	}
	return false, nil
}

func parseStatus(agent string, output []byte) (Status, error) {
	var status Status
	switch agent {
	case "claude-code":
		var plugins []struct {
			ID, Scope, Version string
			Enabled            bool
		}
		if err := json.Unmarshal(output, &plugins); err != nil || plugins == nil {
			return status, fmt.Errorf("claude plugin list returned an invalid JSON list; update Claude Code")
		}
		for _, p := range plugins {
			if p.ID == Plugin && p.Scope == "user" {
				return Status{Installed: true, Enabled: p.Enabled, Version: p.Version}, nil
			}
		}
	case "codex":
		var plugins struct {
			Installed *[]struct {
				PluginID           string `json:"pluginId"`
				Installed, Enabled bool
				Version            string
			}
		}
		if err := json.Unmarshal(output, &plugins); err != nil || plugins.Installed == nil {
			return status, fmt.Errorf("codex plugin list returned invalid installed-plugin JSON; update Codex")
		}
		for _, p := range *plugins.Installed {
			if p.PluginID == Plugin && p.Installed {
				return Status{Installed: true, Enabled: p.Enabled, Version: p.Version}, nil
			}
		}
	default:
		return status, fmt.Errorf("unsupported coding agent %q", agent)
	}
	return status, nil
}

func (c *Client) Install(ctx context.Context, agent string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, ActionTimeout)
	defer cancel()
	if _, err := c.command(ctx, agent, "plugin", "marketplace", "add", Marketplace); err != nil {
		return Status{}, err
	}
	args := []string{"plugin", "add", Plugin, "--json"}
	if agent == "claude-code" {
		args = []string{"plugin", "install", Plugin, "--scope", "user", "--json"}
	}
	if _, err := c.command(ctx, agent, args...); err != nil {
		return Status{}, err
	}
	status, err := c.Status(ctx, agent)
	if err != nil {
		return status, err
	}
	if !status.Installed {
		return status, fmt.Errorf("%s did not report %s installed; inspect its plugin UI", Binary(agent), Plugin)
	}
	if !status.Enabled {
		return status, fmt.Errorf("%s is installed but disabled in %s; enable it and review hook trust in the agent", Plugin, Binary(agent))
	}
	return status, nil
}

func (c *Client) Uninstall(ctx context.Context, agent string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, ActionTimeout)
	defer cancel()
	status, err := c.Status(ctx, agent)
	if err != nil || !status.Installed {
		return status, err
	}
	args := []string{"plugin", "remove", Plugin, "--json"}
	if agent == "claude-code" {
		args = []string{"plugin", "uninstall", Plugin, "--scope", "user", "--json"}
	}
	if _, err := c.command(ctx, agent, args...); err != nil {
		return status, err
	}
	status, err = c.Status(ctx, agent)
	if err == nil && status.Installed {
		err = fmt.Errorf("%s still reports %s installed; inspect its plugin UI", Binary(agent), Plugin)
	}
	return status, err
}
