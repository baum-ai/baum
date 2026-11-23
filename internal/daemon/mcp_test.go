package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeMCP struct{ token string }

func (f fakeMCP) Authenticate(token string) bool { return token == f.token }
func (fakeMCP) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusTeapot)
}

func TestSwapMCP(t *testing.T) {
	var mcp swapMCP
	serve := func() int {
		w := httptest.NewRecorder()
		mcp.ServeHTTP(w, httptest.NewRequest("POST", "/api/mcp", nil))
		return w.Code
	}
	if mcp.Authenticate("") || mcp.Authenticate("t") || serve() != http.StatusNotFound {
		t.Fatal("an unset MCP server admitted a request")
	}
	mcp.set(nil)
	mcp.set(fakeMCP{token: "t"})
	if !mcp.Authenticate("t") || mcp.Authenticate("u") || serve() != http.StatusTeapot {
		t.Fatal("the MCP server set was not used")
	}
}
