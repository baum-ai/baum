package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/daemon"
)

// mcpTokenEnv names the variable that carries the MCP token. The token is
// never a flag, so it stays out of the process list.
const mcpTokenEnv = "BAUM_MCP_TOKEN"

const (
	// maxMCPLine bounds one JSON-RPC message on stdin, as the daemon bounds
	// one request body.
	maxMCPLine = 1 << 20
	// maxMCPResponse bounds one response the bridge relays.
	maxMCPResponse = 16 << 20
	// mcpTimeout bounds one request to the daemon.
	mcpTimeout = 2 * time.Minute
)

// JSON-RPC error codes the bridge answers with when the daemon cannot.
const (
	rpcInvalidRequest = -32600
	rpcServerError    = -32000
)

func newMCPCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve baum's MCP tools to an agent over stdio (agents start this)",
		Long: `baum mcp is the stdio MCP server an agent starts to read what the user sees in
the baum app. baum starts it itself for the agent runs that need it, with
BAUM_MCP_TOKEN set to that run's token; you do not run it by hand.

It forwards every JSON-RPC message the agent writes on stdin, one per line, to
the local baum daemon's /api/mcp route and prints each response on stdout,
one request at a time. It runs no commands and talks to nothing but the local
daemon. The files it touches are the daemon's credential file, read for the
daemon's address (never its token), and the repository registry baum opens
read-only before any command; nothing it does writes to stdout. The token comes
only from the BAUM_MCP_TOKEN environment variable and is never logged. It exits
when stdin closes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := os.Getenv(mcpTokenEnv)
			if token == "" {
				return fmt.Errorf("%s is not set: baum mcp is started by an agent that baum gave a token", mcpTokenEnv)
			}
			parent := cmd.Context()
			if parent == nil {
				parent = context.Background()
			}
			ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
			defer stop()
			bridge := &mcpBridge{
				token:  token,
				origin: daemonOrigin,
				client: &http.Client{Timeout: mcpTimeout},
				out:    cmd.OutOrStdout(),
				errw:   cmd.ErrOrStderr(),
			}
			return bridge.run(ctx, cmd.InOrStdin())
		},
	}
}

// daemonOrigin reads the running daemon's loopback origin. It uses only the
// origin of the credential, never its token.
func daemonOrigin() (string, error) {
	paths, err := daemonPaths()
	if err != nil {
		return "", err
	}
	credential, err := daemon.ReadCredential(paths)
	if err != nil {
		return "", err
	}
	if origin := credential.Origin(); origin != "" {
		return origin, nil
	}
	return "", daemon.ErrNoCredential
}

// mcpBridge relays JSON-RPC messages from stdin to the daemon. It handles one
// message at a time, in order: the next line is not forwarded until the
// daemon has answered the last, so a slow tool call delays the messages behind
// it (a cancellation included).
type mcpBridge struct {
	token string
	// origin reads the daemon's address; called again after a dial failure so
	// a restarted daemon on a new port is found.
	origin func() (string, error)
	client *http.Client
	// out receives only protocol lines; errw receives diagnostics.
	out, errw io.Writer
	cached    string
	reported  map[string]bool
}

// run relays until in closes or ctx ends. Messages are handled one at a time,
// in order.
func (b *mcpBridge) run(ctx context.Context, in io.Reader) error {
	type line struct {
		text    []byte
		tooLong bool
	}
	lines := make(chan line)
	go func() {
		defer close(lines)
		reader := bufio.NewReaderSize(in, 64<<10)
		for {
			text, tooLong, err := readBoundedLine(reader, maxMCPLine)
			if len(text) > 0 || tooLong {
				select {
				case lines <- line{text, tooLong}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case l, ok := <-lines:
			if !ok {
				return nil
			}
			if l.tooLong {
				b.reply(nil, rpcInvalidRequest, "message too large")
				continue
			}
			b.handle(ctx, l.text)
		}
	}
}

// readBoundedLine reads one line of at most limit bytes without its newline.
// A longer line is discarded to its end and reported as tooLong.
func readBoundedLine(r *bufio.Reader, limit int) (text []byte, tooLong bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(text)+len(chunk) > limit+1 {
				tooLong, text = true, nil
			} else {
				text = append(text, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return nil, true, err
		}
		text = bytes.TrimRight(text, "\r\n")
		return text, len(text) > limit, err
	}
}

// handle forwards one message and writes its response, if it has one.
func (b *mcpBridge) handle(ctx context.Context, message []byte) {
	message = bytes.TrimSpace(message)
	if len(message) == 0 {
		return
	}
	// The id decides whether a failure is answered: a notification has none
	// and is never answered.
	var head struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(message, &head)
	status, body, err := b.post(ctx, message)
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return
		}
		b.diagnose("unreachable", "the baum daemon is not reachable: "+err.Error())
		b.fail(head.ID, "the baum daemon is not reachable; is baum running?")
	case status == http.StatusUnauthorized:
		b.diagnose("unauthorized", "the daemon refused the MCP token; it was revoked or the daemon restarted")
		b.fail(head.ID, "the baum MCP token is no longer valid; the conversation or action it belonged to has ended")
	case status == http.StatusAccepted && len(bytes.TrimSpace(body)) == 0:
		// A notification was accepted: nothing to write.
	default:
		var compact bytes.Buffer
		if json.Compact(&compact, body) != nil || compact.Len() == 0 || compact.Bytes()[0] != '{' {
			b.diagnose("non-json", fmt.Sprintf("the daemon answered %d with a body that is not JSON", status))
			b.fail(head.ID, fmt.Sprintf("the baum daemon answered with an unexpected response (HTTP %d)", status))
			return
		}
		b.write(compact.Bytes())
	}
}

// post sends one message to the daemon. When no connection could be made (a
// dial failure such as connection refused) it reads the credential again and
// tries once more, so a daemon that restarted on a new port is found. A
// failure after the request may have reached the daemon (a timeout, a dropped
// connection) is returned as it is and never retried: the daemon may already
// have acted on the message, and a tool call must not run twice.
func (b *mcpBridge) post(ctx context.Context, message []byte) (int, []byte, error) {
	if b.cached == "" {
		origin, err := b.origin()
		if err != nil {
			return 0, nil, err
		}
		b.cached = origin
	}
	status, body, err := b.send(ctx, b.cached, message)
	if err == nil || ctx.Err() != nil || !notConnected(err) {
		return status, body, err
	}
	origin, err := b.origin()
	if err != nil {
		b.cached = ""
		return 0, nil, err
	}
	b.cached = origin
	return b.send(ctx, origin, message)
}

// notConnected reports an error from before any connection to the daemon
// existed, so nothing of the request was sent.
func notConnected(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func (b *mcpBridge) send(ctx context.Context, origin string, message []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/mcp", bytes.NewReader(message))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", agentcontext.ProtocolVersion)
	resp, err := b.client.Do(req)
	if err != nil {
		// The error names the URL, never the token (a header).
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPResponse))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// fail answers a request that has an id with a JSON-RPC error; a notification
// gets nothing.
func (b *mcpBridge) fail(id json.RawMessage, message string) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	b.reply(id, rpcServerError, message)
}

func (b *mcpBridge) reply(id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	encoded, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{code, message}})
	b.write(encoded)
}

// write prints one protocol line.
func (b *mcpBridge) write(line []byte) {
	_, _ = b.out.Write(append(bytes.TrimRight(line, "\n"), '\n'))
}

// diagnose prints each kind of problem once to stderr, so a daemon that stays
// down does not fill the agent's log.
func (b *mcpBridge) diagnose(kind, message string) {
	if b.reported == nil {
		b.reported = map[string]bool{}
	}
	if b.reported[kind] {
		return
	}
	b.reported[kind] = true
	_, _ = fmt.Fprintln(b.errw, "baum mcp:", strings.ReplaceAll(message, b.token, "[token]"))
}
