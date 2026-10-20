package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/anthropic/anthropictest"
)

const testKey = "sk-ant-api03-test-SECRET-KEY-0123456789"

var testSchema = []byte(`{"type":"object","properties":{"schema":{"type":"string","const":"baum.review/1"},"summary":{"type":"string"}},"required":["schema","summary"]}`)

const validDocument = `{"schema":"baum.review/1","summary":"Adds a greeting."}`

// sent is one request the stub received.
type sent struct {
	header http.Header
	raw    []byte
	body   struct {
		Model        string                  `json:"model"`
		MaxTokens    int                     `json:"max_tokens"`
		System       []json.RawMessage       `json:"system"`
		Messages     []rawMessage            `json:"messages"`
		Tools        []struct{ Name string } `json:"tools"`
		CacheControl map[string]any          `json:"cache_control"`
		OutputConfig map[string]any          `json:"output_config"`
	}
}

type rawMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type sentResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Text      string `json:"text"`
	Content   []struct {
		Type, Text string
	} `json:"content"`
}

// results returns the blocks of the request's last user message.
func (s sent) results(t *testing.T) []sentResult {
	t.Helper()
	last := s.body.Messages[len(s.body.Messages)-1]
	if last.Role != "user" {
		t.Fatalf("the last message is %s", last.Role)
	}
	out := make([]sentResult, len(last.Content))
	for i, block := range last.Content {
		if err := json.Unmarshal(block, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (r sentResult) text() string {
	var parts []string
	for _, c := range r.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "")
}

type step func(t *testing.T, request sent, w http.ResponseWriter)

// stub serves scripted steps in order and records every request.
type stub struct {
	t      *testing.T
	mu     sync.Mutex
	steps  []step
	served []sent
	server *httptest.Server
}

func newStub(t *testing.T, steps ...step) *stub {
	s := &stub{t: t, steps: steps}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request sent
		request.header = r.Header.Clone()
		request.raw, _ = io.ReadAll(r.Body)
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if err := json.Unmarshal(request.raw, &request.body); err != nil {
			t.Errorf("request body: %v", err)
		}
		s.mu.Lock()
		n := len(s.served)
		s.served = append(s.served, request)
		s.mu.Unlock()
		if n >= len(s.steps) {
			t.Errorf("unexpected request %d", n+1)
			http.Error(w, "no more steps", http.StatusTeapot)
			return
		}
		s.steps[n](t, request, w)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) requests() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sent(nil), s.served...)
}

func (s *stub) backend() *Backend {
	return &Backend{APIKey: testKey, BaseURL: s.server.URL, retryDelay: func(int, time.Duration) time.Duration { return time.Millisecond }}
}

var messageCount atomic.Int64

// reply streams a message. usage is the usage object's JSON.
func reply(stop, usage string, blocks ...string) step {
	return func(t *testing.T, _ sent, w http.ResponseWriter) {
		message := fmt.Sprintf(`{"id":"msg_%d","type":"message","role":"assistant","model":"claude-opus-5-5","content":[%s],"stop_reason":%q,"usage":%s}`, messageCount.Add(1), strings.Join(blocks, ","), stop, usage)
		if err := anthropictest.WriteMessage(w, message); err != nil {
			t.Error(err)
		}
	}
}

// events writes raw stream events (each the data of one event), then stops.
func events(data ...string) step {
	return func(_ *testing.T, _ sent, w http.ResponseWriter) {
		w.Header().Set("content-type", "text/event-stream")
		for _, d := range data {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", d)
		}
	}
}

const messageStart = `{"type":"message_start","message":{"id":"msg_x","type":"message","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`

func status(code int, body string, header ...string) step {
	return func(_ *testing.T, _ sent, w http.ResponseWriter) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

func raw(body string) step {
	return func(_ *testing.T, _ sent, w http.ResponseWriter) { _, _ = io.WriteString(w, body) }
}

// then runs check on the request before answering with next.
func then(check func(t *testing.T, request sent), next step) step {
	return func(t *testing.T, request sent, w http.ResponseWriter) {
		check(t, request)
		next(t, request, w)
	}
}

func toolUse(id, name, input string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, id, name, input)
}

func submit(id, document string) string { return toolUse(id, structuredOutputTool, document) }

const smallUsage = `{"input_tokens":10,"output_tokens":5}`

func runDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("head/main.go", "package main\n\nfunc main() {\n\tgreet()\n}\n")
	write("head/greet/greet.go", "package greet\n\nfunc Greet() string { return \"hello\" }\n")
	write("merge-base/main.go", "package main\n\nfunc main() {}\n")
	write("bundle.json", `{"diff":[]}`)
	return dir
}

func request(dir string) agent.Request {
	return agent.Request{Directory: dir, Prompt: "Review the pull request.", Schema: testSchema, MaxCalls: 10, MaxTokens: 100000}
}

func TestToolLoopSubmitsTheDocument(t *testing.T) {
	thinking := `{"type":"thinking","thinking":"","signature":"sig-opaque-123"}`
	s := newStub(t,
		reply("tool_use", `{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":0,"cache_creation_input_tokens":3000,"cache_creation":{"ephemeral_5m_input_tokens":3000,"ephemeral_1h_input_tokens":0}}`,
			thinking,
			toolUse("toolu_1", "Read", `{"file_path":"head/main.go","offset":3,"limit":2}`),
			toolUse("toolu_2", "Glob", `{"pattern":"**/*.go","path":"head"}`),
			toolUse("toolu_3", "Grep", `{"pattern":"func [A-Z]","path":"head","glob":"*.go"}`)),
		then(func(t *testing.T, r sent) {
			assistant := r.body.Messages[1]
			var replayed, original any
			_ = json.Unmarshal(assistant.Content[0], &replayed)
			_ = json.Unmarshal([]byte(thinking), &original)
			if assistant.Role != "assistant" || fmt.Sprint(replayed) != fmt.Sprint(original) {
				t.Errorf("the thinking block was not replayed unchanged: %s", assistant.Content[0])
			}
			results := r.results(t)
			if len(results) != 3 {
				t.Fatalf("results = %+v", results)
			}
			for _, res := range results {
				if res.Type != "tool_result" || res.IsError {
					t.Errorf("result %+v", res)
				}
			}
			if got := results[0].text(); got != "     3\tfunc main() {\n     4\t\tgreet()\n[1 more lines; read on with offset 5]\n" {
				t.Errorf("Read = %q", got)
			}
			if got := results[1].text(); got != "head/greet/greet.go\nhead/main.go\n" {
				t.Errorf("Glob = %q", got)
			}
			if got := results[2].text(); got != "head/greet/greet.go:3:func Greet() string { return \"hello\" }\n" {
				t.Errorf("Grep = %q", got)
			}
		}, reply("tool_use", `{"input_tokens":50,"output_tokens":400,"cache_read_input_tokens":3000,"cache_creation_input_tokens":1500,"cache_creation":{"ephemeral_5m_input_tokens":1200,"ephemeral_1h_input_tokens":300}}`,
			submit("toolu_4", validDocument))),
	)
	var logs bytes.Buffer
	req := request(runDir(t))
	req.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	result, err := s.backend().Run(context.Background(), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(result.Document) != validDocument || result.Model != "claude-opus-5-5" || result.ModelSource != "provider_reported" {
		t.Errorf("result = %s %s %s", result.Document, result.Model, result.ModelSource)
	}
	if result.Calls != 2 || result.Tokens != 1000+200+3000+50+400+1500 {
		t.Errorf("calls %d tokens %d", result.Calls, result.Tokens)
	}
	u := result.Usage
	if u.Input != 1050 || u.Output != 600 || u.CacheRead != 3000 || u.CacheCreate != 4500 || u.Unreported {
		t.Errorf("usage = %+v", u)
	}
	if u.CacheCreate1h == nil || *u.CacheCreate1h != 300 || u.Speed == nil || *u.Speed != "standard" {
		t.Errorf("1h %v speed %v", u.CacheCreate1h, u.Speed)
	}
	if u.MinPrompt == nil || *u.MinPrompt != 4000 || u.MaxPrompt == nil || *u.MaxPrompt != 4550 {
		t.Errorf("prompt range %v %v", u.MinPrompt, u.MaxPrompt)
	}
	if len(result.UsageAttempts) != 2 || result.UsageAttempts[1].Model != "claude-opus-5-5" || result.UsageAttempts[1].ChargedTokens != 1950 || result.UsageAttempts[1].Failed {
		t.Errorf("attempts = %+v", result.UsageAttempts)
	}
	var accounting agent.Accounting
	accounting.Add(result, false)
	if accounting.Usage.Unreported || accounting.ChargedTokens != result.Tokens || len(accounting.Attempts) != 2 {
		t.Errorf("accounting = %+v", accounting)
	}

	requests := s.requests()
	first := requests[0]
	if first.header.Get("x-api-key") != testKey || first.header.Get("anthropic-version") != APIVersion {
		t.Errorf("headers = %v", first.header)
	}
	if first.body.Model != DefaultModel || first.body.MaxTokens != maxOutputTokens || first.body.OutputConfig["effort"] != DefaultEffort {
		t.Errorf("model %q max %d config %v", first.body.Model, first.body.MaxTokens, first.body.OutputConfig)
	}
	if first.body.CacheControl["type"] != "ephemeral" || !strings.Contains(string(first.body.Messages[0].Content[0]), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("caching: %v %s", first.body.CacheControl, first.body.Messages[0].Content[0])
	}
	var names []string
	for _, tool := range first.body.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "Read,Glob,Grep,StructuredOutput" {
		t.Errorf("tools = %v", names)
	}
	if !strings.Contains(string(first.raw), `"stream":true`) || first.header.Get("accept") != "text/event-stream" {
		t.Error("the request does not ask for a stream")
	}
	if !strings.Contains(string(first.raw), `"input_schema":`+string(testSchema)) {
		t.Error("StructuredOutput does not take the document schema")
	}
	// The tools and system prompt, the cached prefix, are the same in every call.
	if string(requests[1].body.System[0]) != string(first.body.System[0]) || len(requests[1].body.Tools) != 4 {
		t.Error("the prefix changed between calls")
	}

	encoded, _ := json.Marshal(result.Events)
	for _, leaked := range []string{"greet()", "func [A-Z]", "**/*.go", "baum.review/1", "hello", testKey} {
		if strings.Contains(string(encoded), leaked) || strings.Contains(logs.String(), leaked) {
			t.Errorf("events or logs hold %q", leaked)
		}
	}
	for _, want := range []string{`"tool":"Read","path":"head/main.go","offset":3,"limit":2`, `"kind":"document"`, `"kind":"finished","elapsed_ms"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("events lack %s: %s", want, encoded)
		}
	}
}

func TestUsageBuckets(t *testing.T) {
	cases := []struct {
		name  string
		usage string
		check func(t *testing.T, u agent.Usage)
	}{
		{"split and fast speed reported", `{"input_tokens":5,"output_tokens":7,"cache_read_input_tokens":11,"cache_creation_input_tokens":13,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":10},"speed":"fast"}`, func(t *testing.T, u agent.Usage) {
			if u.Input != 5 || u.Output != 7 || u.CacheRead != 11 || u.CacheCreate != 13 || *u.CacheCreate1h != 10 || *u.Speed != "fast" || *u.MinPrompt != 29 {
				t.Errorf("usage = %+v", u)
			}
		}},
		{"no split: only 5-minute writes are requested", `{"input_tokens":5,"output_tokens":7,"cache_creation_input_tokens":13}`, func(t *testing.T, u agent.Usage) {
			if u.CacheCreate1h == nil || *u.CacheCreate1h != 0 || *u.Speed != "standard" {
				t.Errorf("usage = %+v", u)
			}
		}},
		{"impossible split is unknown", `{"input_tokens":5,"output_tokens":7,"cache_creation_input_tokens":1,"cache_creation":{"ephemeral_1h_input_tokens":4}}`, func(t *testing.T, u agent.Usage) {
			if u.CacheCreate1h != nil {
				t.Errorf("usage = %+v", u)
			}
		}},
		{"unknown speed is unknown", `{"input_tokens":5,"output_tokens":7,"speed":"warp"}`, func(t *testing.T, u agent.Usage) {
			if u.Speed != nil {
				t.Errorf("usage = %+v", u)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStub(t, reply("tool_use", c.usage, submit("t1", validDocument)))
			result, err := s.backend().Run(context.Background(), request(runDir(t)))
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, result.Usage)
			if result.Tokens != int(result.Usage.Input+result.Usage.Output+result.Usage.CacheCreate) {
				t.Errorf("charged %d, cache reads must not count", result.Tokens)
			}
		})
	}
}

func TestSchemaRejectionGoesBackToTheModel(t *testing.T) {
	s := newStub(t,
		reply("tool_use", smallUsage, submit("t1", `{"schema":"baum.review/1"}`)),
		then(func(t *testing.T, r sent) {
			results := r.results(t)
			if len(results) != 1 || !results[0].IsError || !strings.Contains(results[0].text(), "does not match the output schema") {
				t.Errorf("results = %+v", results)
			}
		}, reply("tool_use", smallUsage, submit("t2", validDocument))),
	)
	result, err := s.backend().Run(context.Background(), request(runDir(t)))
	if err != nil || result.StructuredOutputRejections != 1 || result.Calls != 2 {
		t.Fatalf("err %v rejections %d calls %d", err, result.StructuredOutputRejections, result.Calls)
	}
}

func TestEndTurnWithoutDocument(t *testing.T) {
	text := `{"type":"text","text":"Here is my review."}`
	t.Run("nudged into submitting", func(t *testing.T) {
		s := newStub(t,
			reply("end_turn", smallUsage, text),
			then(func(t *testing.T, r sent) {
				if results := r.results(t); len(results) != 1 || results[0].Type != "text" || !strings.Contains(results[0].Text, "StructuredOutput") {
					t.Errorf("nudge = %+v", results)
				}
			}, reply("tool_use", smallUsage, submit("t1", validDocument))),
		)
		if result, err := s.backend().Run(context.Background(), request(runDir(t))); err != nil || result.Calls != 2 {
			t.Fatalf("%v %d", err, result.Calls)
		}
	})
	t.Run("fails after the nudges", func(t *testing.T) {
		s := newStub(t, reply("end_turn", smallUsage, text), reply("end_turn", smallUsage, text), reply("end_turn", smallUsage, text))
		_, err := s.backend().Run(context.Background(), request(runDir(t)))
		var named *Error
		if !errors.As(err, &named) || named.Category != CategoryNoDocument {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTruncatedToolCallIsNotRun(t *testing.T) {
	s := newStub(t,
		reply("max_tokens", smallUsage, toolUse("t1", "Read", `{"file_path":"head/main.go"}`)),
		then(func(t *testing.T, r sent) {
			results := r.results(t)
			if len(results) != 2 || !results[0].IsError || !strings.Contains(results[0].text(), "not run") || results[1].Type != "text" {
				t.Errorf("results = %+v", results)
			}
		}, reply("tool_use", smallUsage, submit("t2", validDocument))),
	)
	if _, err := s.backend().Run(context.Background(), request(runDir(t))); err != nil {
		t.Fatal(err)
	}
}

func TestBudgets(t *testing.T) {
	read := toolUse("t1", "Read", `{"file_path":"head/main.go"}`)
	t.Run("calls", func(t *testing.T) {
		s := newStub(t, reply("tool_use", smallUsage, read))
		req := request(runDir(t))
		req.MaxCalls = 1
		result, err := s.backend().Run(context.Background(), req)
		if err == nil || err.Error() != agent.GenerationBudgetExceededReason("model calls") || result.Calls != 1 || len(s.requests()) != 1 {
			t.Fatalf("err %v calls %d requests %d", err, result.Calls, len(s.requests()))
		}
	})
	t.Run("tokens spent by a response", func(t *testing.T) {
		s := newStub(t, reply("tool_use", `{"input_tokens":900,"output_tokens":200}`, read))
		req := request(runDir(t))
		req.MaxTokens = 1000
		result, err := s.backend().Run(context.Background(), req)
		if err == nil || err.Error() != agent.GenerationBudgetExceededReason("tokens") || result.Tokens != 1100 {
			t.Fatalf("err %v tokens %d", err, result.Tokens)
		}
		if got := s.requests()[0].body.MaxTokens; got != 1000 {
			t.Errorf("max_tokens = %d, want the remaining budget", got)
		}
	})
	t.Run("cache reads are not charged", func(t *testing.T) {
		s := newStub(t, reply("tool_use", `{"input_tokens":10,"output_tokens":10,"cache_read_input_tokens":5000}`, submit("t1", validDocument)))
		req := request(runDir(t))
		req.MaxTokens = 100
		if result, err := s.backend().Run(context.Background(), req); err != nil || result.Tokens != 20 {
			t.Fatalf("err %v tokens %d", err, result.Tokens)
		}
	})
	t.Run("output is bounded by the remaining tokens", func(t *testing.T) {
		s := newStub(t, reply("tool_use", `{"input_tokens":400,"output_tokens":100}`, read), reply("tool_use", smallUsage, submit("t2", validDocument)))
		req := request(runDir(t))
		req.MaxTokens = 2000
		if _, err := s.backend().Run(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if got := s.requests()[1].body.MaxTokens; got != 1500 {
			t.Errorf("second max_tokens = %d", got)
		}
	})
	t.Run("refused requests spend the call budget", func(t *testing.T) {
		overloaded := status(529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
		s := newStub(t, overloaded, overloaded)
		req := request(runDir(t))
		req.MaxCalls = 1
		result, err := s.backend().Run(context.Background(), req)
		if err == nil || !strings.HasPrefix(err.Error(), agent.GenerationBudgetExceededReason("model calls")) || !strings.Contains(err.Error(), "overloaded") {
			t.Fatalf("err = %v", err)
		}
		if len(s.requests()) != 1 || result.Calls != 1 {
			t.Errorf("%d requests, %d calls", len(s.requests()), result.Calls)
		}
	})
	t.Run("nothing left", func(t *testing.T) {
		s := newStub(t)
		for _, req := range []agent.Request{{MaxCalls: 0, MaxTokens: 10}, {MaxCalls: 1, MaxTokens: 0}} {
			req.Directory, req.Schema = runDir(t), testSchema
			if _, err := s.backend().Run(context.Background(), req); err == nil || !strings.HasPrefix(err.Error(), agent.GenerationBudgetExceededReason("")) {
				t.Errorf("err = %v", err)
			}
		}
		if len(s.requests()) != 0 {
			t.Error("a request was sent without budget")
		}
	})
}

func TestCancellation(t *testing.T) {
	arrived := make(chan struct{})
	s := newStub(t, func(_ *testing.T, _ sent, w http.ResponseWriter) {
		close(arrived)
		time.Sleep(2 * time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-arrived
		cancel()
	}()
	started := time.Now()
	result, err := s.backend().Run(ctx, request(runDir(t)))
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("err %v after %s", err, time.Since(started))
	}
	// The request reached the API, so what it cost is unknown, not zero.
	if !result.Usage.Unreported || result.Calls != 1 {
		t.Errorf("usage %+v calls %d", result.Usage, result.Calls)
	}
}

func TestWallClockBoundsRetries(t *testing.T) {
	limited := status(429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, "retry-after", "30")
	s := newStub(t, limited)
	backend := s.backend()
	backend.retryDelay = nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err := backend.Run(ctx, request(runDir(t)))
	var named *Error
	if !errors.As(err, &named) || named.Category != CategoryRateLimit || !strings.Contains(err.Error(), "time budget") || time.Since(started) > time.Second {
		t.Fatalf("err %v after %s", err, time.Since(started))
	}

	// Waits that fit are spent under the run's deadline, and the retries stop
	// before it passes.
	s = newStub(t, limited, limited, limited, limited, limited)
	backend = s.backend()
	backend.retryDelay = func(int, time.Duration) time.Duration { return 200 * time.Millisecond }
	ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started = time.Now()
	_, err = backend.Run(ctx, request(runDir(t)))
	if !errors.As(err, &named) || named.Category != CategoryRateLimit || time.Since(started) > 600*time.Millisecond || len(s.requests()) != 3 {
		t.Fatalf("err %v after %s with %d requests", err, time.Since(started), len(s.requests()))
	}
}

func TestHTTPFailures(t *testing.T) {
	overloaded := status(529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	cases := []struct {
		name     string
		steps    []step
		category Category
		requests int
		want     string
	}{
		{"401 is not retried", []step{status(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)}, CategoryAuthentication, 1, "401"},
		{"403 is not retried", []step{status(403, `{"type":"error","error":{"type":"permission_error","message":"no"}}`)}, CategoryAuthentication, 1, "403"},
		{"529 is retried, then named", []step{overloaded, overloaded, overloaded, overloaded, overloaded}, CategoryOverloaded, 5, "overloaded"},
		{"429 is retried, then named", []step{status(429, ``), status(429, ``), status(429, ``), status(429, ``), status(429, ``)}, CategoryRateLimit, 5, "429"},
		{"500 is retried, then named", []step{status(500, ``), status(502, ``), status(503, ``), status(504, ``), status(500, `{"type":"error","error":{"type":"api_error","message":"x"}}`)}, CategoryServer, 5, "api_error"},
		{"400 keeps the API's explanation", []step{status(400, `{"type":"error","error":{"type":"invalid_request_error","message":"model: unknown model claude-nope"}}`)}, CategoryInvalidRequest, 1, "unknown model claude-nope"},
		{"404", []step{status(404, `not json`)}, CategoryInvalidRequest, 1, "refused"},
		{"a redirect is not followed", []step{status(307, ``, "location", "http://127.0.0.1:1/v1/messages")}, CategoryInvalidRequest, 1, "307"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStub(t, c.steps...)
			result, err := s.backend().Run(context.Background(), request(runDir(t)))
			var named *Error
			if !errors.As(err, &named) || named.Category != c.category || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v", err)
			}
			if got := len(s.requests()); got != c.requests {
				t.Errorf("%d requests, want %d", got, c.requests)
			}
			// Every answered request is a call; refused ones cost nothing.
			if result.Calls != c.requests || result.Usage.Unreported || !result.Usage.IsZero() {
				t.Errorf("calls %d usage %+v", result.Calls, result.Usage)
			}
			if status := agent.LastErrorStatus(result.Events); status != string(c.category) {
				t.Errorf("error event status = %q", status)
			}
		})
	}
	t.Run("429 then success, honouring retry-after", func(t *testing.T) {
		s := newStub(t, status(429, ``, "retry-after", "7"), reply("tool_use", smallUsage, submit("t1", validDocument)))
		backend := s.backend()
		var asked []time.Duration
		backend.retryDelay = func(_ int, retryAfter time.Duration) time.Duration {
			asked = append(asked, retryAfter)
			return time.Millisecond
		}
		result, err := backend.Run(context.Background(), request(runDir(t)))
		if err != nil || result.Calls != 2 || len(asked) != 1 || asked[0] != 7*time.Second {
			t.Fatalf("err %v calls %d asked %v", err, result.Calls, asked)
		}
	})
	t.Run("the default wait honours retry-after within a cap", func(t *testing.T) {
		b := &Backend{}
		if got := b.wait(0, 3*time.Second); got != 3*time.Second {
			t.Errorf("wait = %s", got)
		}
		if got := b.wait(9, 0); got < 32*time.Second || got > maxRetryWait {
			t.Errorf("backoff = %s", got)
		}
		header := http.Header{}
		for value, want := range map[string]time.Duration{"2": 2 * time.Second, "0.5": 500 * time.Millisecond, "9999": maxRetryWait, "-1": 0, "NaN": 0, "soon": 0} {
			header.Set("retry-after", value)
			if got := parseRetryAfter(header); got != want {
				t.Errorf("retry-after %s = %s", value, got)
			}
		}
	})
}

func TestMalformedResponses(t *testing.T) {
	cases := []struct {
		name     string
		step     step
		category Category
		counted  bool
	}{
		{"not JSON", raw(`<html>`), CategoryMalformed, true},
		{"not a message", raw(`{"type":"error"}`), CategoryMalformed, true},
		{"no usage", raw(`{"type":"message","model":"m","content":[],"stop_reason":"end_turn"}`), CategoryMalformed, true},
		{"negative usage", reply("end_turn", `{"input_tokens":-1,"output_tokens":1}`, `{"type":"text","text":"x"}`), CategoryMalformed, true},
		{"unknown stop reason", reply("pause_turn", smallUsage, `{"type":"text","text":"x"}`), CategoryMalformed, false},
		{"tool use without an id", reply("tool_use", smallUsage, `{"type":"tool_use","name":"Read","input":{}}`), CategoryMalformed, false},
		{"untyped block", reply("tool_use", smallUsage, `{"text":"x"}`), CategoryMalformed, false},
		{"empty content", reply("end_turn", smallUsage), CategoryMalformed, false},
		{"refusal", reply("refusal", smallUsage), CategoryRefusal, false},
		{"context window", reply("model_context_window_exceeded", smallUsage), CategoryContextWindow, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStub(t, c.step)
			result, err := s.backend().Run(context.Background(), request(runDir(t)))
			var named *Error
			if !errors.As(err, &named) || named.Category != c.category {
				t.Fatalf("err = %v", err)
			}
			if result.Calls != 1 || len(s.requests()) != 1 {
				t.Errorf("calls %d requests %d: a malformed answer is a call and is not retried here", result.Calls, len(s.requests()))
			}
			// A response without usable usage leaves the cost unknown.
			if result.Usage.Unreported != c.counted {
				t.Errorf("unreported = %v", result.Usage.Unreported)
			}
		})
	}
}

func TestTheAPIKeyNeverAppears(t *testing.T) {
	echo := `{"type":"error","error":{"type":"invalid_request_error","message":"bad key ` + testKey + `"}}`
	cases := map[string][]step{
		"401 echoing the key":      {status(401, echo)},
		"400 echoing the key":      {status(400, echo)},
		"529 echoing the key":      {status(529, echo), status(529, echo), status(529, echo), status(529, echo), status(529, echo)},
		"malformed with the key":   {raw(`{"type":"message","model":"` + testKey + `","content":[],"stop_reason":"x"}`)},
		"stop reason with the key": {reply(testKey, smallUsage, `{"type":"text","text":"x"}`)},
		"tool call naming the key": {reply("tool_use", smallUsage, toolUse(testKey, testKey, `{}`)), status(400, `{}`)},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, steps...)
			var logs bytes.Buffer
			req := request(runDir(t))
			req.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			result, err := s.backend().Run(context.Background(), req)
			if err == nil {
				t.Fatal("no error")
			}
			encoded, _ := json.Marshal(result.Events)
			for where, text := range map[string]string{"error": err.Error(), "events": string(encoded), "logs": logs.String()} {
				if strings.Contains(text, testKey) || strings.Contains(text, "SECRET-KEY") {
					t.Errorf("the key is in the %s: %s", where, text)
				}
			}
		})
	}
	t.Run("a transport failure", func(t *testing.T) {
		backend := &Backend{APIKey: testKey, BaseURL: "http://127.0.0.1:1", retryDelay: func(int, time.Duration) time.Duration { return time.Millisecond }}
		result, err := backend.Run(context.Background(), request(runDir(t)))
		var named *Error
		if !errors.As(err, &named) || named.Category != CategoryTransport || strings.Contains(err.Error(), testKey) {
			t.Fatalf("err = %v", err)
		}
		// Nothing was sent, so nothing was spent.
		if result.Usage.Unreported {
			t.Error("a refused connection marked the usage unknown")
		}
	})
	t.Run("the endpoint's key is used and not leaked", func(t *testing.T) {
		const other = "sk-ant-endpoint-OTHER-KEY"
		s := newStub(t, status(401, `{"type":"error","error":{"type":"authentication_error","message":"`+other+`"}}`))
		req := request(runDir(t))
		req.Endpoint = &agent.Endpoint{BaseURL: s.server.URL, Key: other}
		_, err := (&Backend{APIKey: testKey, BaseURL: "http://127.0.0.1:1"}).Run(context.Background(), req)
		if err == nil || strings.Contains(err.Error(), other) || s.requests()[0].header.Get("x-api-key") != other {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCheckAndSettings(t *testing.T) {
	if _, err := (&Backend{}).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY is not set") {
		t.Errorf("no key: %v", err)
	}
	if _, err := (&Backend{APIKey: "k"}).Check(context.Background()); err != nil {
		t.Errorf("key: %v", err)
	}
	if _, err := (&Backend{}).Run(context.Background(), request(runDir(t))); err == nil {
		t.Error("a run without a key started")
	}
	s := newStub(t, then(func(t *testing.T, r sent) {
		if r.body.Model != "claude-haiku-5-5" || r.body.OutputConfig != nil {
			t.Errorf("model %q config %v: another model runs at its own default effort", r.body.Model, r.body.OutputConfig)
		}
	}, reply("tool_use", smallUsage, submit("t1", validDocument))))
	backend := s.backend()
	backend.Model = "claude-haiku-5-5"
	if _, err := backend.Run(context.Background(), request(runDir(t))); err != nil {
		t.Fatal(err)
	}
	if (&Backend{}).Name() != agent.BackendAnthropic {
		t.Error("name")
	}
}
