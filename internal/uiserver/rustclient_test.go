package uiserver

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// The desktop app reads /api/events with a raw HTTP/1.0 request. This sends
// exactly that request (see oss/desktop/src/notifications/events.rs) to a real
// listener: the body must be plain SSE, not chunked, and the cursor beyond the
// latest event must give a reset and then only new events.
func TestDesktopClientsRawHTTP10Request(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	if err := s.Publish("notice", map[string]any{"kind": "old"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = s.Serve(ctx) }()

	conn, err := net.Dial("tcp", strings.TrimPrefix(s.origin, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	_, err = fmt.Fprintf(conn, "GET /api/events HTTP/1.0\r\nHost: %s\r\nAuthorization: Bearer %s\r\nAccept: text/event-stream\r\nLast-Event-ID: %d\r\n\r\n",
		strings.TrimPrefix(s.origin, "http://"), s.launch, uint64(18446744073709551615))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(status, "HTTP/1.0 200") {
		t.Fatalf("%q %v", status, err)
	}
	var head, body strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
		head.WriteString(strings.ToLower(line))
	}
	if strings.Contains(head.String(), "transfer-encoding") || !strings.Contains(head.String(), "text/event-stream") {
		t.Fatalf("the body must be unchunked SSE: %s", head.String())
	}
	published := false
	for !strings.Contains(body.String(), `"kind":"new"`) {
		if !published && strings.Contains(body.String(), "event: reset") && strings.HasSuffix(body.String(), "\n\n") {
			published = true
			if err := s.Publish("notice", map[string]any{"kind": "new"}); err != nil {
				t.Fatal(err)
			}
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("%v: %s", err, body.String())
		}
		body.WriteString(line)
	}
	got := body.String()
	if strings.Contains(got, "old") || !strings.Contains(got, "event: reset") || !strings.Contains(got, "event: notice") {
		t.Fatal(got)
	}
	// A chunked body would show chunk sizes between lines.
	if strings.Contains(got, "\r\n") {
		t.Fatalf("chunked framing in %q", got)
	}
}
