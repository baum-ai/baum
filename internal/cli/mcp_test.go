package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/uiserver"
)

// mcpDaemon serves the real app server with the real MCP server behind
// /api/mcp and returns the MCP server and the app origin.
func mcpDaemon(t *testing.T) (*agentcontext.Server, string) {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{}`) })
	mcp := agentcontext.New(agentcontext.Options{API: api, Version: "test"})
	s, err := uiserver.New(uiserver.Options{API: api, MCP: mcp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return mcp, s.Origin()
}

// credentialAt makes the commands read a daemon credential for origin from a
// private state directory, and returns a function that points it elsewhere.
func credentialAt(t *testing.T, origin string) (point func(origin string)) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bc")
	if err != nil {
		t.Fatal(err)
	}
	paths := daemon.Paths{State: filepath.Join(dir, "s"), Socket: filepath.Join(dir, "d.sock")}
	old := daemonPaths
	daemonPaths = func() (daemon.Paths, error) { return paths, nil }
	t.Cleanup(func() { daemonPaths = old; _ = os.RemoveAll(dir) })
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	point = func(origin string) {
		t.Helper()
		// The token half is the daemon's secret; the bridge must never use it.
		data, _ := json.Marshal(map[string]string{"url": origin + "/#token=" + strings.Repeat("d", 43)})
		if err := os.WriteFile(paths.CredentialFile(), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	point(origin)
	return point
}

// closedOrigin is a loopback origin nothing listens on.
func closedOrigin(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + l.Addr().String()
	_ = l.Close()
	return origin
}

// bridgeProcess runs "baum mcp" with pipes.
type bridgeProcess struct {
	stdin  io.WriteCloser
	lines  *bufio.Scanner
	stderr *bytes.Buffer
	done   chan error
}

func startBridge(t *testing.T, token string) *bridgeProcess {
	t.Helper()
	t.Setenv(mcpTokenEnv, token)
	// OS pipes buffer, as an agent's do; an io.Pipe would deadlock a bridge
	// that answers while the test is still writing.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &bridgeProcess{stdin: inW, lines: bufio.NewScanner(outR), stderr: &bytes.Buffer{}, done: make(chan error, 1)}
	p.lines.Buffer(nil, 1<<20)
	go func() {
		err := Run([]string{"mcp"}, inR, outW, p.stderr)
		_ = outW.Close()
		_ = inR.Close()
		p.done <- err
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return p
}

func (p *bridgeProcess) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(p.stdin, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

// next reads one stdout line, failing if none comes.
func (p *bridgeProcess) next(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	got := make(chan bool, 1)
	go func() { got <- p.lines.Scan() }()
	select {
	case ok := <-got:
		if !ok {
			t.Fatalf("stdout closed: %v", p.lines.Err())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no response line")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(p.lines.Bytes(), &message); err != nil {
		t.Fatalf("stdout line %q is not a JSON object: %v", p.lines.Text(), err)
	}
	return message
}

// finish closes stdin and returns the exit error and every stdout line left.
func (p *bridgeProcess) finish(t *testing.T) (error, []string) {
	t.Helper()
	_ = p.stdin.Close()
	var rest []string
	for p.lines.Scan() {
		rest = append(rest, p.lines.Text())
	}
	select {
	case err := <-p.done:
		return err, rest
	case <-time.After(10 * time.Second):
		t.Fatal("baum mcp did not exit when stdin closed")
		return nil, nil
	}
}

const (
	initializeMessage = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	initializedNotice = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	toolsListMessage  = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	currentPageCall   = `{"jsonrpc":"2.0","id":"three","method":"tools/call","params":{"name":"current_page","arguments":{}}}`
)

func TestMCPBridgeRelaysAnAgentSession(t *testing.T) {
	mcp, origin := mcpDaemon(t)
	credentialAt(t, origin)
	token, id, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	if err := mcp.Registry.SetPage(id, agentcontext.PageContext{Page: agentcontext.PageInbox}); err != nil {
		t.Fatal(err)
	}
	p := startBridge(t, token)
	for _, line := range []string{initializeMessage, initializedNotice, toolsListMessage, currentPageCall} {
		p.send(t, line)
	}
	first := p.next(t)
	if string(first["id"]) != "1" || first["error"] != nil || !strings.Contains(string(first["result"]), `"protocolVersion":"2025-06-18"`) {
		t.Errorf("initialize response %v", first)
	}
	second := p.next(t)
	if string(second["id"]) != "2" || !strings.Contains(string(second["result"]), `"current_page"`) {
		t.Errorf("tools/list response %v", second)
	}
	third := p.next(t)
	if string(third["id"]) != `"three"` || third["error"] != nil || !strings.Contains(string(third["result"]), `inbox`) {
		t.Errorf("tools/call response %v", third)
	}
	err, rest := p.finish(t)
	if err != nil {
		t.Errorf("exit on EOF: %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("extra stdout lines %q (the notification must get none)", rest)
	}
	if strings.Contains(p.stderr.String(), token) {
		t.Errorf("stderr holds the token: %s", p.stderr)
	}
}

func TestMCPBridgeAnswersRevokedTokenPerRequest(t *testing.T) {
	mcp, origin := mcpDaemon(t)
	credentialAt(t, origin)
	token, _, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	revoke()
	p := startBridge(t, token)
	p.send(t, initializeMessage)
	p.send(t, initializedNotice)
	p.send(t, currentPageCall)
	for _, want := range []string{"1", `"three"`} {
		reply := p.next(t)
		var rpc struct{ Code int }
		_ = json.Unmarshal(reply["error"], &rpc)
		if string(reply["id"]) != want || rpc.Code != -32000 || !strings.Contains(string(reply["error"]), "token is no longer valid") {
			t.Errorf("reply for %s: %v", want, reply)
		}
	}
	err, rest := p.finish(t)
	if err != nil || len(rest) != 0 {
		t.Errorf("exit %v with extra lines %q", err, rest)
	}
	if strings.Count(p.stderr.String(), "baum mcp:") != 1 || strings.Contains(p.stderr.String(), token) {
		t.Errorf("stderr %q", p.stderr)
	}
}

// A daemon that is down answers each request with an error and the bridge
// keeps going; once the credential names a live daemon (it restarted on
// another port) the next message reaches it.
func TestMCPBridgeSurvivesAnUnreachableDaemonAndFindsItAgain(t *testing.T) {
	mcp, origin := mcpDaemon(t)
	token, _, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	point := credentialAt(t, closedOrigin(t))
	p := startBridge(t, token)
	p.send(t, initializeMessage)
	p.send(t, initializedNotice)
	p.send(t, toolsListMessage)
	for _, want := range []string{"1", "2"} {
		reply := p.next(t)
		if string(reply["id"]) != want || !strings.Contains(string(reply["error"]), "not reachable") {
			t.Errorf("reply for %s: %v", want, reply)
		}
	}
	point(origin)
	p.send(t, toolsListMessage)
	if reply := p.next(t); string(reply["id"]) != "2" || reply["error"] != nil || !strings.Contains(string(reply["result"]), "current_page") {
		t.Errorf("after the daemon came back: %v", reply)
	}
	err, rest := p.finish(t)
	if err != nil || len(rest) != 0 {
		t.Errorf("exit %v with extra lines %q", err, rest)
	}
}

func TestMCPBridgeRefusesOversizedAndKeepsGoing(t *testing.T) {
	mcp, origin := mcpDaemon(t)
	credentialAt(t, origin)
	token, _, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	p := startBridge(t, token)
	p.send(t, `{"jsonrpc":"2.0","id":1,"method":"x","params":"`+strings.Repeat("a", maxMCPLine+10)+`"}`)
	reply := p.next(t)
	if string(reply["id"]) != "null" || !strings.Contains(string(reply["error"]), "too large") {
		t.Errorf("oversized reply %v", reply)
	}
	p.send(t, toolsListMessage)
	if reply := p.next(t); string(reply["id"]) != "2" || reply["error"] != nil {
		t.Errorf("after the oversized line: %v", reply)
	}
	if err, _ := p.finish(t); err != nil {
		t.Error(err)
	}
}

// A dial failure means nothing was sent: the bridge reads the credential again
// and retries once, so a daemon that moved is found.
func TestMCPBridgePostRetriesWhenNoConnectionWasMade(t *testing.T) {
	var hits atomic.Int32
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer live.Close()
	origins := []string{closedOrigin(t), live.URL}
	reads := 0
	b := &mcpBridge{token: "t", client: &http.Client{Timeout: 5 * time.Second}, origin: func() (string, error) {
		reads++
		return origins[min(reads-1, len(origins)-1)], nil
	}}
	status, _, err := b.post(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil || status != 200 || hits.Load() != 1 || reads != 2 {
		t.Errorf("status %d, err %v, hits %d, credential reads %d", status, err, hits.Load(), reads)
	}
}

// A request that reached the daemon, which then dropped the connection, may
// have been acted on: the bridge reports the failure and sends nothing again.
func TestMCPBridgePostDoesNotRetryAfterTheRequestWasSent(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			// Read the request, then drop the connection without answering.
			_, _ = bufio.NewReader(conn).ReadString('}')
			_ = conn.Close()
		}
	}()
	reads := 0
	b := &mcpBridge{token: "t", client: &http.Client{Timeout: 5 * time.Second}, origin: func() (string, error) {
		reads++
		return "http://" + listener.Addr().String(), nil
	}}
	if _, _, err := b.post(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`)); err == nil {
		t.Fatal("a dropped connection was not an error")
	}
	if accepted.Load() != 1 || reads != 1 {
		t.Errorf("connections %d, credential reads %d: the request was sent again", accepted.Load(), reads)
	}
}

func TestMCPNeedsATokenInTheEnvironmentOnly(t *testing.T) {
	t.Setenv(mcpTokenEnv, "")
	var out, errOut bytes.Buffer
	err := Run([]string{"mcp"}, strings.NewReader(initializeMessage+"\n"), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), mcpTokenEnv) {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout %q", out.String())
	}
	if err := Run([]string{"mcp", "--token", "x"}, strings.NewReader(""), &out, &errOut); err == nil {
		t.Error("a --token flag was accepted")
	}
}
