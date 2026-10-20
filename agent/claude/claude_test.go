//go:build unix

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// testEndpoint lets the Isolated profile run the fake executables below.
var testEndpoint = &agent.Endpoint{BaseURL: "https://api.anthropic.example", Key: "test-key"}

func TestClaudeProcessEnvironmentAndUsageBudgets(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > arguments.txt\nenv > environment.txt\ncat > prompt.txt\n" + "printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"id\":\"m1\",\"model\":\"fixture\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}'\n" + "printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"structured_output\":{\"ok\":true},\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}'\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "private database")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "private github")
	t.Setenv("STORAGE_SECRET_ACCESS_KEY", "private storage")
	t.Setenv("GIT_REPOSITORY_INTERNAL_TOKEN", "private repository")
	backend := &Backend{Binary: script, Model: "fixture", Profile: Isolated}
	dir := t.TempDir()
	access := &agent.Endpoint{BaseURL: "https://proxy.example/anthropic", Key: "run-token"}
	result, err := backend.Run(context.Background(), agent.Request{Directory: dir, Prompt: "generate", Schema: []byte(`{}`), MaxCalls: 2, MaxTokens: 20, Endpoint: access})
	if err != nil || result.Calls != 1 || result.Tokens != 12 || string(result.Document) != `{"ok":true}` {
		t.Fatalf("%+v %v", result, err)
	}
	arguments, _ := os.ReadFile(filepath.Join(dir, "arguments.txt"))
	if !bytes.Contains(arguments, []byte("--restricted\n")) || !bytes.Contains(arguments, []byte("--tools\nRead,Glob,Grep\n")) || bytes.Contains(arguments, []byte("--effort\n")) || bytes.Contains(arguments, []byte("--allowedTools")) || !bytes.Contains(arguments, []byte("--json-schema\n{}\n")) {
		t.Fatalf("incorrect harness flags: %s", arguments)
	}
	if !bytes.Contains(arguments, []byte("StructuredOutput tool exactly once")) || !bytes.Contains(arguments, []byte(`never wrap them in an "input" key`)) || bytes.Contains(arguments, []byte("as its input")) || bytes.Contains(arguments, []byte("Return a single JSON object")) {
		t.Fatalf("system prompt must ask for one StructuredOutput call, not JSON text: %s", arguments)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if string(prompt) != "generate" {
		t.Fatal("schema was duplicated in the prompt")
	}
	environment, _ := os.ReadFile(filepath.Join(dir, "environment.txt"))
	for _, secret := range []string{"DATABASE_URL", "GITHUB_APP_PRIVATE_KEY", "STORAGE_SECRET_ACCESS_KEY", "GIT_REPOSITORY_INTERNAL_TOKEN"} {
		if bytes.Contains(environment, []byte(secret)) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if !bytes.Contains(environment, []byte("CLAUDE_CODE_DISABLE_THINKING=1")) || !bytes.Contains(environment, []byte("MAX_THINKING_TOKENS=0")) || !bytes.Contains(environment, []byte("CLAUDE_CODE_MAX_OUTPUT_TOKENS=32000")) {
		t.Fatal("generation thinking/output limits were not applied")
	}
	if !bytes.Contains(environment, []byte("ANTHROPIC_API_KEY=run-token")) {
		t.Fatal("missing the run's model access")
	}
	_, err = backend.Run(context.Background(), agent.Request{Directory: dir, Prompt: "generate", Schema: []byte(`{}`), MaxCalls: 2, MaxTokens: 5, Endpoint: access})
	if err == nil || err.Error() != agent.GenerationBudgetExceededReason("tokens") {
		t.Fatalf("%v", err)
	}
}

func TestClaudeProcessUsesOnlyStructuredOutput(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		valid        bool
	}{
		{"structured", `{"ok":true}`, true},
		{"missing", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []string{
				`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"malformed partial output"}}}`,
				`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}}`,
				`{"type":"assistant","message":{"id":"m1","model":"fixture","usage":{"input_tokens":10,"output_tokens":3}}}`,
				`{"type":"result","subtype":"success","result":"{\"wrong\":true}","structured_output":` + tc.output + `,"usage":{"input_tokens":10,"output_tokens":3}}`,
			}
			script := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n"+strings.Join(events, "\n")+"\nEVENTS\n"), 0700); err != nil {
				t.Fatal(err)
			}
			result, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: 2, MaxTokens: 30})
			if (err == nil) != tc.valid || tc.valid && string(result.Document) != tc.output || result.Calls != 1 || result.Tokens != 13 {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

func TestClaudeUsageTakesEachCallsFinalReport(t *testing.T) {
	// With --include-partial-messages an assistant snapshot carries the output
	// count when it was taken; message_delta and the result carry the final one.
	call := func(id, start, snapshot, delta string) []string {
		events := []string{`{"type":"stream_event","event":{"type":"message_start","message":{"id":"` + id + `","model":"fixture","usage":` + start + `}}}`}
		if snapshot != "" {
			events = append(events, `{"type":"assistant","message":{"id":"`+id+`","model":"fixture","usage":`+snapshot+`}}`)
		}
		if delta != "" {
			events = append(events, `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":`+delta+`}}`,
				`{"type":"stream_event","event":{"type":"message_stop"}}`)
		}
		return events
	}
	first := call("m1", `{"input_tokens":3,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"output_tokens":1}`,
		`{"input_tokens":3,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"output_tokens":10}`,
		`{"input_tokens":3,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"output_tokens":30893}`)
	second := call("m2", `{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":1}`,
		`{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":4}`,
		`{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":200}`)
	resultEvent := func(usage string) string {
		return `{"type":"result","subtype":"success","structured_output":{"ok":true},"usage":` + usage + `}`
	}
	join := func(parts ...[]string) []string {
		var events []string
		for _, part := range parts {
			events = append(events, part...)
		}
		return events
	}
	// m2's stream dies after message_start; Claude Code retries it as m3.
	aborted := second[:1]
	retried := call("m3", `{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":1}`,
		`{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":4}`,
		`{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":200}`)
	const noDocument = "claude code returned no structured document"
	for _, tc := range []struct {
		name   string
		events []string
		// maxCalls and maxTokens default to 3 and 100000.
		maxCalls, maxTokens int
		// wantErr is the run's error, empty for success.
		wantErr string
		calls   int
		usage   agent.Usage
		// logged is the model_usage diagnostics as ID:output: one per call,
		// at its message_stop, else at the end of the run.
		logged []string
	}{
		{name: "final delta replaces the snapshot", events: join(first, []string{resultEvent(`{"input_tokens":3,"cache_creation_input_tokens":5000,"output_tokens":30893}`)}),
			calls: 1, usage: agent.Usage{Input: 3, Output: 30893, CacheCreate: 5000}, logged: []string{"m1:30893"}},
		{name: "several deltas log the last", events: []string{
			`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"fixture","usage":{"input_tokens":3,"output_tokens":1}}}}`,
			`{"type":"stream_event","event":{"type":"message_delta","delta":{},"usage":{"output_tokens":50}}}`,
			`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":60}}}`,
			`{"type":"stream_event","event":{"type":"message_stop"}}`,
			resultEvent(`{}`),
		}, calls: 1, usage: agent.Usage{Input: 3, Output: 60}, logged: []string{"m1:60"}},
		{name: "calls are summed", events: join(first, second, []string{resultEvent(`{"input_tokens":10,"cache_creation_input_tokens":5000,"cache_read_input_tokens":5000,"output_tokens":31093}`)}),
			calls: 2, usage: agent.Usage{Input: 10, Output: 31093, CacheRead: 5000, CacheCreate: 5000}, logged: []string{"m1:30893", "m2:200"}},
		{name: "result total covers an unstreamed call", events: []string{
			`{"type":"assistant","message":{"id":"m1","model":"fixture","usage":{"input_tokens":3,"output_tokens":10}}}`,
			resultEvent(`{"input_tokens":3,"output_tokens":30893}`),
		}, calls: 1, usage: agent.Usage{Input: 3, Output: 30893}, logged: []string{"m1:10"}},
		// A delta carrying only output, or a bucket as 0, keeps
		// message_start's input and cache buckets.
		{name: "delta carries only output", events: join(call("m1", `{"input_tokens":3,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"output_tokens":1}`,
			`{"input_tokens":3,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"output_tokens":10}`, `{"output_tokens":30893}`), []string{resultEvent(`{}`)}),
			calls: 1, usage: agent.Usage{Input: 3, Output: 30893, CacheCreate: 5000}, logged: []string{"m1:30893"}},
		{name: "delta reports input as 0", events: join(call("m1", `{"input_tokens":3,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"output_tokens":1}`,
			"", `{"input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":30893}`), []string{resultEvent(`{}`)}),
			calls: 1, usage: agent.Usage{Input: 3, Output: 30893, CacheCreate: 5000}, logged: []string{"m1:30893"}},
		// The final output trips the token budget at the call's
		// message_delta, before any result.
		{name: "token budget trips at a delta", events: join(first, second, []string{resultEvent(`{}`)}), maxTokens: 10000,
			wantErr: agent.GenerationBudgetExceededReason("tokens"), calls: 1, usage: agent.Usage{Input: 3, Output: 30893, CacheCreate: 5000}, logged: []string{"m1:30893"}},
		// Claude Code's total leaves out the call whose stream died; the
		// stream sum is kept.
		{name: "result smaller than the stream sum", events: join(first, aborted, retried, []string{resultEvent(`{"input_tokens":10,"cache_creation_input_tokens":5000,"cache_read_input_tokens":5000,"output_tokens":31093}`)}),
			calls: 2, usage: agent.Usage{Input: 17, Output: 31094, CacheRead: 10000, CacheCreate: 5000, Unreported: true}, logged: []string{"m1:30893", "m3:200", "m2:1"}},
		// The aborted call is not a turn: the run stays within two calls.
		{name: "aborted and retried call is counted once", events: join(first, aborted, retried, []string{resultEvent(`{}`)}), maxCalls: 2,
			calls: 2, usage: agent.Usage{Input: 17, Output: 31094, CacheRead: 10000, CacheCreate: 5000, Unreported: true}, logged: []string{"m1:30893", "m3:200", "m2:1"}},
		// A run cut off before its result keeps each finished call's final
		// usage and the latest report of the call in flight.
		{name: "run ends without a result", events: join(first, second[:2]), wantErr: noDocument,
			calls: 2, usage: agent.Usage{Input: 10, Output: 30893 + 4, CacheRead: 5000, CacheCreate: 5000}, logged: []string{"m1:30893", "m2:4"}},
		{name: "run ends before the call in flight is snapshotted", events: join(first, aborted), wantErr: noDocument,
			calls: 1, usage: agent.Usage{Input: 10, Output: 30893 + 1, CacheRead: 5000, CacheCreate: 5000}, logged: []string{"m1:30893", "m2:1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n"+strings.Join(tc.events, "\n")+"\nEVENTS\n"), 0700); err != nil {
				t.Fatal(err)
			}
			maxCalls, maxTokens := 3, 100000
			if tc.maxCalls != 0 {
				maxCalls = tc.maxCalls
			}
			if tc.maxTokens != 0 {
				maxTokens = tc.maxTokens
			}
			result, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: maxCalls, MaxTokens: maxTokens})
			if (err == nil) != (tc.wantErr == "") || err != nil && err.Error() != tc.wantErr {
				t.Fatalf("error %v, want %q", err, tc.wantErr)
			}
			// The per-call prompt range is checked apart (usage_test.go).
			got := result.Usage
			got.MinPrompt, got.MaxPrompt = nil, nil
			tc.usage.Unreported = tc.usage.Unreported || tc.wantErr != ""
			if result.Calls != tc.calls || got != tc.usage || result.Model != "fixture" {
				t.Fatalf("got %d calls, usage %+v, model %q", result.Calls, result.Usage, result.Model)
			}
			if want := int(tc.usage.Input + tc.usage.Output + tc.usage.CacheCreate); result.Tokens != want {
				t.Fatalf("tokens %d, want %d", result.Tokens, want)
			}
			var logged []string
			for _, event := range result.Events {
				if event.Kind == "model_usage" {
					logged = append(logged, event.ID+":"+strconv.Itoa(event.Output))
				}
			}
			if !slices.Equal(logged, tc.logged) {
				t.Fatalf("model_usage events %v, want %v", logged, tc.logged)
			}
		})
	}
}

func TestClaudeDiagnosticsSurviveFailureWithoutRawContent(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	events := []string{
		`{"type":"system","subtype":"init","tools":["Read","Glob","Grep","StructuredOutput"]}`,
		`{"type":"assistant","message":{"id":"m1","model":"fixture","usage":{"input_tokens":10,"cache_read_input_tokens":20,"output_tokens":2},"content":[{"type":"text","text":"private assistant content"},{"type":"tool_use","id":"read1","name":"Read","input":{"file_path":"bundle.json","offset":1,"limit":200}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"read1","is_error":true,"content":"File content exceeds maximum allowed tokens. private repository content"}]}}`,
		`{"type":"result","subtype":"error_max_turns","is_error":true}`,
	}
	body := "#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n" + strings.Join(events, "\n") + "\nEVENTS\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	r, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: 2, MaxTokens: 100, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	// Cache reads (20) are reported but not charged against the token budget.
	if err == nil || r.Calls != 1 || r.Tokens != 12 || r.Model != "fixture" {
		t.Fatalf("%+v %v", r, err)
	}
	trace, _ := json.Marshal(r.Events)
	if !bytes.Contains(trace, []byte("read_token_limit")) || !bytes.Contains(trace, []byte("cache_read_tokens")) || !bytes.Contains(trace, []byte("tool_call")) || r.Events[len(r.Events)-1].Kind != "finished" {
		t.Fatalf("missing diagnostics: %s", trace)
	}
	if bytes.Contains(trace, []byte("private")) || bytes.Contains(logs.Bytes(), []byte("private")) {
		t.Fatal("raw content leaked")
	}
}

// A failed result is classified by the HTTP status of the provider response it
// ended on, so a refusal is not retried. The results are the shape Claude Code
// 2.1 prints after an API error.
func TestClaudeClassifiesTheProviderStatusOfAFailedResult(t *testing.T) {
	for _, tt := range []struct {
		name, result, want string
		rejected           bool
	}{
		{"bad request", `{"type":"result","subtype":"success","is_error":true,"api_error_status":400,"result":"API Error: 400 private repository text","terminal_reason":"api_error"}`, "invalid_request", true},
		{"forbidden", `{"type":"result","subtype":"success","is_error":true,"api_error_status":403,"result":"Failed to authenticate. API Error: 403 private repository text"}`, "authentication_error", true},
		{"model not found", `{"type":"result","subtype":"success","is_error":true,"api_error_status":404,"result":"There's an issue with the selected model"}`, "not_found", true},
		{"overloaded", `{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"result":"API Error: 529 private repository text"}`, "provider_overloaded", false},
		{"server error", `{"type":"result","subtype":"success","is_error":true,"api_error_status":500,"result":"API Error: 500 private repository text"}`, "error", false},
		{"server error quoting a refusal", `{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":502,"errors":["authentication 401 invalid_request"]}`, "error", false},
		{"no status", `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["boom"]}`, "error", false},
		// Text alone is classified for the diagnostics, never a refusal.
		{"text that reads as a refusal", `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["API Error: 401 authentication failed"]}`, "authentication_error", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "claude")
			body := "#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n" + tt.result + "\nEVENTS\n"
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			r, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: 2, MaxTokens: 100})
			if status := agent.LastErrorStatus(r.Events); status != tt.want {
				t.Fatalf("status = %q, want %q", status, tt.want)
			}
			var rejected *agent.RejectedError
			if errors.As(err, &rejected) != tt.rejected || (!tt.rejected && (err == nil || err.Error() != agent.ReasonProviderUnavailable)) {
				t.Fatalf("err = %v, want rejected %t", err, tt.rejected)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("the error exposes the result: %v", err)
			}
		})
	}
}

// Each tool use runs from its tool_use to its tool_result, so the tool timeout
// bounds a tool that never returns.
func TestClaudeReportsToolsToTheActivity(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	events := []string{
		`{"type":"assistant","message":{"id":"m1","model":"fixture","usage":{"input_tokens":10,"output_tokens":2},"content":[{"type":"tool_use","id":"read1","name":"Read","input":{"file_path":"a.go"}},{"type":"tool_use","id":"grep1","name":"Grep","input":{"path":"."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"read1","content":"ok"}]}}`,
		`{"type":"result","subtype":"error_max_turns","is_error":true}`,
	}
	body := "#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n" + strings.Join(events, "\n") + "\nEVENTS\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	activity := agent.NewActivity()
	_, _ = (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: 2, MaxTokens: 100, Activity: activity})
	// Read returned; Grep never did.
	if running := activity.Running(); running != 1 {
		t.Fatalf("%d tools running, want the Grep that never returned", running)
	}
}

func TestClaudeLogsStructuredOutputRejectionsBounded(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	detail := "Output validation failed: sections[0].risks[0].id " + strings.Repeat("x", 600) + "\nprivate tail"
	rejection, _ := json.Marshal(detail)
	events := []string{
		`{"type":"assistant","message":{"id":"m1","model":"fixture","usage":{"input_tokens":10,"output_tokens":2},"content":[{"type":"tool_use","id":"so1","name":"StructuredOutput","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"so1","is_error":true,"content":` + string(rejection) + `}]}}`,
		`{"type":"assistant","message":{"id":"m2","model":"fixture","usage":{"input_tokens":10,"output_tokens":2},"content":[{"type":"tool_use","id":"read1","name":"Read","input":{"file_path":"bundle.json"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"read1","is_error":true,"content":"File does not exist. private repository content"}]}}`,
		`{"type":"result","subtype":"success","structured_output":{"ok":true},"usage":{"input_tokens":20,"output_tokens":4}}`,
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n"+strings.Join(events, "\n")+"\nEVENTS\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	r, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: 3, MaxTokens: 100, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil || r.Calls != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	var statuses []string
	for _, event := range r.Events {
		if event.Kind == "tool_result" {
			statuses = append(statuses, event.Tool+":"+event.Status)
		}
	}
	if strings.Join(statuses, ",") != "StructuredOutput:schema_rejected,Read:file_not_found" {
		t.Fatalf("tool result diagnostics: %v", statuses)
	}
	if !bytes.Contains(logs.Bytes(), []byte("structured output rejected")) || !bytes.Contains(logs.Bytes(), []byte("sections[0].risks[0].id")) {
		t.Fatalf("schema rejection detail missing from the private log: %s", logs.String())
	}
	if bytes.Contains(logs.Bytes(), []byte("private")) || bytes.Contains(logs.Bytes(), []byte(`\n`)) {
		t.Fatal("rejection detail must be bounded and free of other tool output")
	}
}
