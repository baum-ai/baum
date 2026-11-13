//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

const (
	mcpToken = "mcp-token-0123456789abcdef"
	mcpURL   = "http://127.0.0.1:43123/api/mcp"
)

func testMCP() *agent.MCPServer {
	return &agent.MCPServer{URL: mcpURL, Token: mcpToken}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// Without an MCP server the flags are the ones every run has had.
func TestWithoutMCPTheArgumentsAreUnchanged(t *testing.T) {
	tail := []string{"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	backend := &Backend{Model: "m"}
	for name, args := range map[string][]string{
		"review": backend.args(agent.Request{MaxCalls: 3, Schema: []byte(`{}`)}),
		"fix":    backend.fixArgs(agent.Request{MaxCalls: 3}),
	} {
		if !slices.Equal(args[len(args)-3:], tail) {
			t.Errorf("%s ends %q", name, args[len(args)-3:])
		}
		for _, arg := range args {
			if strings.Contains(arg, "mcp__") || arg == "--allowedTools" {
				t.Errorf("%s: unexpected %q", name, arg)
			}
		}
	}
}

func TestMCPNamesOnlyBaumAndNoToken(t *testing.T) {
	backend := &Backend{Model: "m"}
	request := agent.Request{MaxCalls: 3, Schema: []byte(`{}`), MCP: testMCP()}
	for name, args := range map[string][]string{"review": backend.args(request), "fix": backend.fixArgs(request)} {
		if !slices.Contains(args, "--strict-mcp-config") {
			t.Errorf("%s: no --strict-mcp-config in %q", name, args)
		}
		at := slices.Index(args, "--mcp-config")
		if at < 0 {
			t.Fatalf("%s: no --mcp-config in %q", name, args)
		}
		var config struct {
			MCPServers map[string]struct {
				Type    string
				URL     string
				Headers map[string]string
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(args[at+1]), &config); err != nil {
			t.Fatal(err)
		}
		baum, ok := config.MCPServers["baum"]
		// The header names the variable; Claude Code expands it.
		if len(config.MCPServers) != 1 || !ok || baum.Type != "http" || baum.URL != mcpURL ||
			len(baum.Headers) != 1 || baum.Headers["Authorization"] != "Bearer ${BAUM_MCP_TOKEN}" {
			t.Errorf("%s: servers %+v", name, config.MCPServers)
		}
		if at := slices.Index(args, "--allowedTools"); at < 0 || args[at+1] != "mcp__baum" {
			t.Errorf("%s: --allowedTools missing in %q", name, args)
		}
		// The built-in tools and restricted mode stay as they were.
		if !slices.Contains(args, "--restricted") || !slices.Contains(args, "--tools") {
			t.Errorf("%s: lost --restricted or --tools: %q", name, args)
		}
		for _, arg := range args {
			if strings.Contains(arg, mcpToken) {
				t.Errorf("%s: the token is in the arguments: %q", name, arg)
			}
		}
	}
}

func TestMCPTokenIsInTheEnvironmentOnceAndWinsOverAgentEnv(t *testing.T) {
	script := fakeClaude(t, "2.1.296 (Claude Code)", succeeding(`env > environment.txt; printf '%s\n' "$@" > arguments.txt`))
	backend := &Backend{Binary: script, Env: []string{"BAUM_MCP_TOKEN=user-supplied", "OTHER=1"}}
	dir := t.TempDir()
	run := request(dir, nil)
	run.MCP = testMCP()
	if _, err := backend.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, dir, "environment.txt")
	if strings.Count(body, "BAUM_MCP_TOKEN=") != 1 || !strings.Contains(body, "BAUM_MCP_TOKEN="+mcpToken+"\n") || strings.Contains(body, "user-supplied") {
		t.Errorf("environment: %s", body)
	}
	if strings.Contains(readFile(t, dir, "arguments.txt"), mcpToken) {
		t.Error("the token reached the argument list")
	}

	// Without an MCP server the variable is neither added nor kept from the
	// ambient environment.
	t.Setenv("BAUM_MCP_TOKEN", "ambient")
	dir = t.TempDir()
	if _, err := (&Backend{Binary: script}).Run(context.Background(), request(dir, nil)); err != nil {
		t.Fatal(err)
	}
	if body := readFile(t, dir, "environment.txt"); strings.Contains(body, "BAUM_MCP_TOKEN") {
		t.Errorf("environment: %s", body)
	}
}

func TestMCPRefusesWhatItCannotRun(t *testing.T) {
	run := request(t.TempDir(), &agent.Endpoint{Key: "k"})
	run.MCP = testMCP()
	if _, err := (&Backend{Profile: Isolated, Binary: "unused"}).Run(context.Background(), run); err == nil || !strings.Contains(err.Error(), "MCP") {
		t.Errorf("the isolated profile ran with an MCP server: %v", err)
	}
	for _, server := range []*agent.MCPServer{{Token: "t"}, {URL: mcpURL}, {URL: "http://example.com:80/api/mcp", Token: "t"}, {URL: mcpURL, Token: "a\nb"}} {
		run.MCP = server
		if _, err := (&Backend{Binary: "unused"}).Run(context.Background(), run); err == nil {
			t.Errorf("accepted %+v", server)
		}
	}
}
