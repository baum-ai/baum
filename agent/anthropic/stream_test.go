package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

// A stream that breaks once the API has answered is never sent again: the
// call counts, its usage is unknown, and the run fails with a named error.
func TestBrokenStreamsAreNotRetried(t *testing.T) {
	textStart := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	textDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`
	cases := []struct {
		name     string
		step     step
		category Category
		want     string
	}{
		{"cut mid-response", events(messageStart, textStart, textDelta), CategoryTransport, "ended before the message was complete"},
		{"error event mid-response", events(messageStart, textStart, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), CategoryOverloaded, "during the response"},
		{"not JSON", events(messageStart, `{oops`), CategoryMalformed, "not JSON"},
		{"no message_start", events(textStart), CategoryMalformed, "did not start with message_start"},
		{"unknown delta", events(messageStart, textStart, `{"type":"content_block_delta","index":0,"delta":{"type":"warp_delta","x":1}}`), CategoryMalformed, "unknown content delta"},
		{"delta for a block never opened", events(messageStart, `{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"x"}}`), CategoryMalformed, "not open"},
		{"invalid tool input", events(messageStart, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"Read","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_"}}`,
			`{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`, `{"type":"message_stop"}`), CategoryMalformed, "not JSON"},
		{"block still open at message_stop", events(messageStart, textStart, `{"type":"message_stop"}`), CategoryMalformed, "still open"},
		{"a line over the limit", events(messageStart, strings.Repeat("x", maxDocumentBytes+1)), CategoryMalformed, "stream line is over"},
		{"empty body", events(), CategoryMalformed, "carried no message"},
		{"JSON instead of a stream", raw(`{"type":"message"}`), CategoryMalformed, "not an event stream"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStub(t, c.step)
			result, err := s.backend().Run(context.Background(), request(runDir(t)))
			var named *Error
			if !errors.As(err, &named) || named.Category != c.category || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v", err)
			}
			if len(s.requests()) != 1 || result.Calls != 1 || !result.Usage.Unreported {
				t.Errorf("requests %d calls %d unreported %v", len(s.requests()), result.Calls, result.Usage.Unreported)
			}
		})
	}
}

func TestStalledStreamIsDropped(t *testing.T) {
	s := newStub(t, func(_ *testing.T, _ sent, w http.ResponseWriter) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", messageStart)
		w.(http.Flusher).Flush()
		time.Sleep(2 * time.Second)
	})
	backend := s.backend()
	backend.idleTimeout = 100 * time.Millisecond
	started := time.Now()
	result, err := backend.Run(context.Background(), request(runDir(t)))
	var named *Error
	if !errors.As(err, &named) || named.Category != CategoryTransport || !strings.Contains(err.Error(), "no stream event") || time.Since(started) > time.Second {
		t.Fatalf("err %v after %s", err, time.Since(started))
	}
	if len(s.requests()) != 1 || result.Calls != 1 || !result.Usage.Unreported || result.Model != "claude-opus-5-5" {
		t.Errorf("requests %d calls %d usage %+v model %q", len(s.requests()), result.Calls, result.Usage, result.Model)
	}
}

// A request that got no response at all may have run: it counts as a call,
// leaves the usage unknown, and is retried while calls remain.
func TestMissingResponseCountsAndIsRetried(t *testing.T) {
	silent := func(_ *testing.T, _ sent, _ http.ResponseWriter) { time.Sleep(time.Second) }
	s := newStub(t, silent, reply("tool_use", smallUsage, submit("t1", validDocument)))
	backend := s.backend()
	backend.headerTimeout = 100 * time.Millisecond
	result, err := backend.Run(context.Background(), request(runDir(t)))
	if err != nil || result.Calls != 2 || !result.Usage.Unreported || string(result.Document) != validDocument {
		t.Fatalf("err %v calls %d usage %+v", err, result.Calls, result.Usage)
	}

	// With one call allowed, the silent attempt spends it.
	s = newStub(t, silent)
	backend = s.backend()
	backend.headerTimeout = 100 * time.Millisecond
	req := request(runDir(t))
	req.MaxCalls = 1
	if _, err := backend.Run(context.Background(), req); err == nil || !strings.HasPrefix(err.Error(), "generation budget exceeded: model calls") || len(s.requests()) != 1 {
		t.Fatalf("err %v after %d requests", err, len(s.requests()))
	}
}

func TestStreamAssemblesTheMessage(t *testing.T) {
	var out streamed
	stream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"m","type":"message","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":7,"cache_read_input_tokens":3,"output_tokens":1}}}`,
		"",
		": a comment",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		"",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		"",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		"",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"Read","input":{}}}`,
		"",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":"}}`,
		"",
		`data: {"type":"ping"}`,
		"",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`,
		"",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		`data: {"type":"some_future_event"}`,
		"",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	if err := readStream(strings.NewReader(stream), &out); err != nil || !out.complete {
		t.Fatalf("err %v complete %v", err, out.complete)
	}
	m := out.message
	if m.StopReason != "tool_use" || len(m.Content) != 2 || *m.Usage.Input != 7 || *m.Usage.CacheRead != 3 || *m.Usage.Output != 42 {
		t.Fatalf("message = %+v", m)
	}
	if got := string(m.Content[0]); got != `{"signature":"sig","thinking":"hmm","type":"thinking"}` {
		t.Errorf("thinking = %s", got)
	}
	if got := string(m.Content[1]); got != `{"id":"t","input":{"file_path":"a.go"},"name":"Read","type":"tool_use"}` {
		t.Errorf("tool_use = %s", got)
	}
}

// A response cut off at its output limit, or declined mid-response, can leave
// a tool call's input incomplete or its block open. The stop reason decides:
// the truncated call is not run and the model is asked to go on, and a
// refusal stays a refusal; neither is a malformed response.
func TestCutShortStreamsFollowTheStopReason(t *testing.T) {
	submitStart := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"StructuredOutput","input":{}}}`
	partial := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"schema\":\"baum.rev"}}`
	maxTokens := `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":32000}}`
	notRun := func(t *testing.T, r sent) {
		results := r.results(t)
		if len(results) != 2 || !results[0].IsError || !strings.Contains(results[0].text(), "not run") {
			t.Errorf("results = %+v", results)
		}
		// The truncated call goes back with an empty input.
		if !strings.Contains(string(r.body.Messages[1].Content[0]), `"input":{}`) {
			t.Errorf("replayed block = %s", r.body.Messages[1].Content[0])
		}
	}
	for name, cut := range map[string]step{
		"block left open":             events(messageStart, submitStart, partial, maxTokens, `{"type":"message_stop"}`),
		"block closed, input invalid": events(messageStart, submitStart, partial, `{"type":"content_block_stop","index":0}`, maxTokens, `{"type":"message_stop"}`),
		"no message_stop":             events(messageStart, submitStart, partial, maxTokens),
	} {
		t.Run("max_tokens, "+name, func(t *testing.T) {
			s := newStub(t, cut, then(notRun, reply("tool_use", smallUsage, submit("t2", validDocument))))
			result, err := s.backend().Run(context.Background(), request(runDir(t)))
			if err != nil || result.Calls != 2 || result.Usage.Unreported || result.Usage.Output != 32000+5 || string(result.Document) != validDocument {
				t.Fatalf("err %v calls %d usage %+v", err, result.Calls, result.Usage)
			}
		})
	}
	t.Run("refusal with an open block", func(t *testing.T) {
		s := newStub(t, events(messageStart, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":3}}`, `{"type":"message_stop"}`))
		result, err := s.backend().Run(context.Background(), request(runDir(t)))
		var rejected *agent.RejectedError
		var named *Error
		if !errors.As(err, &rejected) || !errors.As(err, &named) || named.Category != CategoryRefusal {
			t.Fatalf("err = %v", err)
		}
		if len(s.requests()) != 1 || result.Calls != 1 || result.Usage.Unreported {
			t.Errorf("requests %d calls %d usage %+v", len(s.requests()), result.Calls, result.Usage)
		}
	})
}

func TestOversizedStream(t *testing.T) {
	reader := &countingReader{r: strings.NewReader(strings.Repeat("x", maxResponseBytes+10)), watch: func() {}}
	if _, err := io.Copy(io.Discard, reader); !errors.Is(err, errTooLarge) {
		t.Errorf("err = %v", err)
	}
}
