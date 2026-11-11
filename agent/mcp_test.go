package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPServerValidate(t *testing.T) {
	abs := filepath.Join(os.TempDir(), "baum")
	for name, server := range map[string]MCPServer{
		"empty command":    {Token: "t"},
		"relative command": {Command: "baum", Token: "t"},
		"dot command":      {Command: "./baum", Token: "t"},
		"no token":         {Command: abs},
		"multi-line token": {Command: abs, Token: "a\nb"},
		"NUL in token":     {Command: abs, Token: "a\x00b"},
	} {
		if server.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, server)
		}
	}
	if err := (&MCPServer{Command: abs, Args: []string{"mcp"}, Token: "t"}).Validate(); err != nil {
		t.Errorf("valid server refused: %v", err)
	}
}

func TestWithMCPTokenReplacesAnyEntry(t *testing.T) {
	got := WithMCPToken([]string{"A=1", MCPTokenEnv + "=user", "B=2"}, &MCPServer{Token: "real"})
	want := []string{"A=1", "B=2", MCPTokenEnv + "=real"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if got := WithMCPToken([]string{"A=1"}, nil); len(got) != 1 {
		t.Errorf("nil server changed the environment: %q", got)
	}
}
