package agent

import "testing"

func TestMCPServerValidate(t *testing.T) {
	const ok = "http://127.0.0.1:43123/api/mcp"
	for name, server := range map[string]MCPServer{
		"no URL":           {Token: "t"},
		"https":            {URL: "https://127.0.0.1:43123/api/mcp", Token: "t"},
		"no port":          {URL: "http://127.0.0.1/api/mcp", Token: "t"},
		"not loopback":     {URL: "http://192.168.1.2:43123/api/mcp", Token: "t"},
		"host name":        {URL: "http://localhost:43123/api/mcp", Token: "t"},
		"credentials":      {URL: "http://u:p@127.0.0.1:43123/api/mcp", Token: "t"},
		"query":            {URL: ok + "?a=1", Token: "t"},
		"relative":         {URL: "/api/mcp", Token: "t"},
		"no token":         {URL: ok},
		"multi-line token": {URL: ok, Token: "a\nb"},
		"NUL in token":     {URL: ok, Token: "a\x00b"},
	} {
		if server.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, server)
		}
	}
	for _, valid := range []string{ok, "http://[::1]:43123/api/mcp"} {
		if err := (&MCPServer{URL: valid, Token: "t"}).Validate(); err != nil {
			t.Errorf("%s refused: %v", valid, err)
		}
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
