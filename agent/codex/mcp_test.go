//go:build unix

package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

const mcpToken = "mcp-token-0123456789abcdef"

func testMCP() *agent.MCPServer {
	return &agent.MCPServer{URL: "http://127.0.0.1:43123/api/mcp", Token: mcpToken}
}

func runWith(t *testing.T, backend *Backend, fix bool, mcp *agent.MCPServer) (args []string, env string) {
	t.Helper()
	dir := t.TempDir()
	request := schemaRequest(dir, nil)
	request.MCP = mcp
	var err error
	if fix {
		_, err = backend.Fix(context.Background(), request)
	} else {
		_, err = backend.Run(context.Background(), request)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "environment.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return readLines(t, dir, "arguments.txt"), string(body)
}

func TestMCPConfiguresOnlyBaumAndKeepsTheTokenOutOfArguments(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", recording)
	for name, fix := range map[string]bool{"review (read-only)": false, "fix (workspace-write)": true} {
		args, env := runWith(t, &Backend{Binary: script, Model: "m", Env: []string{"BAUM_MCP_TOKEN=user-supplied"}}, fix, testMCP())
		for _, want := range []string{`mcp_servers.baum.url="http://127.0.0.1:43123/api/mcp"`, `mcp_servers.baum.bearer_token_env_var="BAUM_MCP_TOKEN"`} {
			at := slices.Index(args, want)
			if at < 1 || args[at-1] != "-c" {
				t.Errorf("%s: missing -c %s in %q", name, want, args)
			}
		}
		for _, flag := range []string{"--ignore-user-config", "--ephemeral"} {
			if !slices.Contains(args, flag) {
				t.Errorf("%s: lost %s", name, flag)
			}
		}
		for _, arg := range args {
			if strings.Contains(arg, mcpToken) || strings.Contains(arg, "user-supplied") {
				t.Errorf("%s: token in argument %q", name, arg)
			}
		}
		if strings.Count(env, "BAUM_MCP_TOKEN=") != 1 || !strings.Contains(env, "BAUM_MCP_TOKEN="+mcpToken+"\n") {
			t.Errorf("%s: environment %s", name, env)
		}
		sandbox := "read-only"
		if fix {
			sandbox = "workspace-write"
		}
		if at := slices.Index(args, "--sandbox"); at < 0 || args[at+1] != sandbox {
			t.Errorf("%s: sandbox in %q", name, args)
		}
	}
}

func TestWithoutMCPNoServerIsConfigured(t *testing.T) {
	t.Setenv("BAUM_MCP_TOKEN", "ambient")
	script := fakeCodex(t, "codex-cli 0.162.1", recording)
	args, env := runWith(t, &Backend{Binary: script, Model: "m"}, false, nil)
	for _, arg := range args {
		if strings.Contains(arg, "mcp") {
			t.Errorf("unexpected %q", arg)
		}
	}
	if strings.Contains(env, "BAUM_MCP_TOKEN") {
		t.Errorf("environment: %s", env)
	}
}

func TestMCPRefusesWhatItCannotRun(t *testing.T) {
	dir := t.TempDir()
	request := schemaRequest(dir, &agent.Endpoint{Key: "k"})
	request.MCP = testMCP()
	if _, err := (&Backend{Profile: Container, Binary: "unused"}).Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "MCP") {
		t.Errorf("the container profile ran with an MCP server: %v", err)
	}
	request.MCP = &agent.MCPServer{URL: "http://127.0.0.1:43123/api/mcp"}
	if _, err := (&Backend{Binary: "unused"}).Run(context.Background(), request); err == nil {
		t.Error("accepted a server without a token")
	}
}
