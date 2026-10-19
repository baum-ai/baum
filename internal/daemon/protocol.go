package daemon

import (
	"encoding/json"
	"fmt"
)

// The control protocol: a client connects to the socket and writes one JSON
// request per line; the daemon answers each with one JSON response line, in
// order. A line is at most maxLine bytes. Every request names the protocol
// the client speaks.
//
//   - "hello" (any protocol) answers with the daemon's Status: its protocol,
//     version, pid and start time. Clients use it to decide whether to keep,
//     replace or refuse the daemon.
//   - "stop" (any protocol) asks the daemon to stop gracefully; it answers
//     before it begins.
//   - "ping" answers ok.
//
// Every other method requires the client's protocol to equal the daemon's;
// otherwise the answer is the error code "protocol_mismatch" together with
// the daemon's Status. Methods added later (event ingestion from hooks) are
// registered through Options.Methods.
type Request struct {
	Protocol int             `json:"protocol"`
	Method   string          `json:"method"`
	Client   string          `json:"client,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// Response answers one Request. Status is set for "hello", "stop" and a
// protocol mismatch.
type Response struct {
	OK     bool            `json:"ok"`
	Status *Status         `json:"status,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a failed request. Code is stable; Message is for people.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Error codes.
const (
	CodeProtocolMismatch = "protocol_mismatch"
	CodeUnknownMethod    = "unknown_method"
	CodeBadRequest       = "bad_request"
	CodeFailed           = "failed"
)

const maxLine = 64 << 10

// IncompatibleError is a running daemon this client cannot use or replace: it
// speaks a newer protocol.
type IncompatibleError struct {
	Daemon Status
	Client string
}

func (e *IncompatibleError) Error() string {
	return fmt.Sprintf("the running baum daemon (version %s, pid %d) speaks control protocol %d, but this baum (version %s) speaks protocol %d; upgrade baum, or stop the daemon with `baum daemon stop`",
		e.Daemon.Version, e.Daemon.PID, e.Daemon.Protocol, e.Client, Protocol)
}
