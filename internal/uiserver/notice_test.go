package uiserver

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The desktop app subscribes without wanting history: a cursor beyond the
// latest event gets a reset carrying the latest id, then only new events.
func TestNoticeEventsReachOnlyLateSubscribers(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.listener.Close() }()
	if err := s.Publish("notice", map[string]any{"kind": "old"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = s.Serve(ctx) }()
	r, _ := http.NewRequestWithContext(ctx, "GET", s.origin+"/api/events", nil)
	r.Header.Set("Authorization", "Bearer "+s.launch)
	r.Header.Set("Last-Event-ID", "18446744073709551615")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	var got strings.Builder
	published := false
	for !strings.Contains(got.String(), `"kind":"new"`) {
		// Only after the reset has fixed the stream's position does a new event count.
		if !published && strings.Contains(got.String(), "event: reset") && strings.HasSuffix(got.String(), "\n\n") {
			published = true
			if err := s.Publish("notice", map[string]any{"kind": "new"}); err != nil {
				t.Fatal(err)
			}
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("%v: %s", err, got.String())
		}
		got.WriteString(line)
	}
	if strings.Contains(got.String(), "old") || !strings.Contains(got.String(), "event: reset") || !strings.Contains(got.String(), "event: notice") {
		t.Fatal(got.String())
	}
}
