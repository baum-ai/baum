package apiclient

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Protocol is the version of the hosted API contract this client speaks. One
// baum binary runs against servers of many releases, so the two agree on this
// number rather than on release versions: raise it when the client starts to
// depend on an API change a server of an earlier release does not have, or
// stops working with one it used to. A server built from the same commit
// accepts it, because it advertises this value as its maximum.
const Protocol = 1

// The handshake headers. Every request names the client's protocol, and every
// response of a server that knows the handshake names the range it accepts.
const (
	ProtocolHeader    = "X-Baum-CLI-Protocol"
	MinProtocolHeader = "X-Baum-CLI-Protocol-Min"
	MaxProtocolHeader = "X-Baum-CLI-Protocol-Max"
)

// legacyServerProtocol is the protocol of a server that predates the
// handshake and so sends no range: it accepts exactly what clients of its time
// spoke.
const legacyServerProtocol = 1

// IncompatibleServerError says the client and the server it called cannot work
// together, and which side has to be updated.
type IncompatibleServerError struct {
	Server   string
	Client   int
	Min, Max int
	// Legacy is a server that predates the handshake.
	Legacy bool
}

func (e *IncompatibleServerError) Error() string {
	if e.Client < e.Min {
		return fmt.Sprintf("this baum is too old for %s: it speaks API protocol %d and the server needs %d to %d; update baum", e.Server, e.Client, e.Min, e.Max)
	}
	if e.Legacy {
		return fmt.Sprintf("this baum is too new for %s: it speaks API protocol %d and the server predates protocol checks (protocol %d); ask its administrator to update it, or use an older baum", e.Server, e.Client, e.Max)
	}
	return fmt.Sprintf("this baum is too new for %s: it speaks API protocol %d and the server supports %d to %d; ask its administrator to update it, or use an older baum", e.Server, e.Client, e.Min, e.Max)
}

// checkProtocol compares the server's advertised range with Protocol.
func checkProtocol(resp *http.Response, server string) error {
	return checkProtocolFor(Protocol, resp, server)
}

// checkProtocolFor compares the range resp advertises with the client's
// protocol. A response without a range comes from a server that predates the
// handshake, which counts as legacyServerProtocol, so today's servers keep
// working until the client moves past it. A server error without a range may
// come from a proxy in front of the server instead, so it proves nothing. A
// range that does not parse is ignored rather than trusted either way.
func checkProtocolFor(client int, resp *http.Response, server string) error {
	minValue, maxValue := resp.Header.Get(MinProtocolHeader), resp.Header.Get(MaxProtocolHeader)
	if minValue == "" && maxValue == "" {
		if client > legacyServerProtocol && resp.StatusCode < http.StatusInternalServerError {
			return &IncompatibleServerError{Server: server, Client: client, Min: legacyServerProtocol, Max: legacyServerProtocol, Legacy: true}
		}
		return nil
	}
	minimum, minErr := strconv.Atoi(strings.TrimSpace(minValue))
	maximum, maxErr := strconv.Atoi(strings.TrimSpace(maxValue))
	if minErr != nil || maxErr != nil || minimum > maximum {
		return nil
	}
	if client < minimum || client > maximum {
		return &IncompatibleServerError{Server: server, Client: client, Min: minimum, Max: maximum}
	}
	return nil
}
