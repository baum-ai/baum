package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestWaitingMethodHoldsItsConnectionAndEndsWithItsClient(t *testing.T) {
	paths := testPaths(t)
	release := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	runInProcess(t, paths, Options{Version: "v1.2.3", Waits: map[string]Method{
		"later": func(ctx context.Context, params json.RawMessage) (any, error) {
			select {
			case <-release:
				return params, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		"hold": func(ctx context.Context, _ json.RawMessage) (any, error) {
			<-ctx.Done()
			cancelled <- struct{}{}
			return nil, ctx.Err()
		},
	}})
	client := &Client{Paths: paths}

	answered := make(chan Response, 1)
	go func() {
		response, err := client.Wait(context.Background(), "later", map[string]int{"a": 1})
		if err != nil {
			t.Error(err)
		}
		answered <- response
	}()
	// Other connections are served while one waits.
	time.Sleep(50 * time.Millisecond)
	if _, err := client.Hello(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case response := <-answered:
		if !response.OK || string(response.Result) != `{"a":1}` {
			t.Fatalf("answer %+v", response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}

	// A client that goes away cancels its request.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Wait(ctx, "hold", nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not notice the client leaving")
	}
	if err := <-done; err == nil {
		t.Fatal("a cancelled wait reported success")
	}

	// One of another protocol is refused as usual.
	if r := rawCall(t, paths, `{"protocol":99,"method":"hold"}`); r.Error == nil || r.Error.Code != CodeProtocolMismatch {
		t.Fatalf("mismatch %+v", r)
	}
}
