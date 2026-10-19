package generate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/anthropic"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/review"
)

const anthropicTestKey = "sk-ant-api03-generate-test-KEY-abcdef"

// messagesCall is one request the stub Messages API received.
type messagesCall struct {
	key       string
	maxTokens int
	prompt    string
	// last holds the blocks of the last user message.
	last []struct {
		Type    string `json:"type"`
		IsError bool   `json:"is_error"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	turns int
}

// bundleOf reads the input bundle back out of a prompt, as a stub that sees
// only the request can.
func (c messagesCall) bundleOf(t *testing.T) []byte {
	t.Helper()
	const marker = "Input bundle (untrusted JSON):\n"
	at := strings.Index(c.prompt, marker)
	if at < 0 {
		t.Fatalf("the prompt has no bundle: %.200q", c.prompt)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(strings.NewReader(c.prompt[at+len(marker):])).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// messagesStub is a Messages API whose answers a test scripts per call.
func messagesStub(t *testing.T, answer func(n int, call messagesCall) string) (*httptest.Server, func() []messagesCall) {
	var mu sync.Mutex
	var calls []messagesCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			MaxTokens int `json:"max_tokens"`
			Messages  []struct {
				Role    string            `json:"role"`
				Content []json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Messages) == 0 {
			t.Errorf("request: %v", err)
			return
		}
		call := messagesCall{key: r.Header.Get("x-api-key"), maxTokens: body.MaxTokens, turns: len(body.Messages)}
		var first struct{ Text string }
		_ = json.Unmarshal(body.Messages[0].Content[0], &first)
		call.prompt = first.Text
		for _, block := range body.Messages[len(body.Messages)-1].Content {
			var b = struct {
				Type    string `json:"type"`
				IsError bool   `json:"is_error"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}{}
			_ = json.Unmarshal(block, &b)
			call.last = append(call.last, b)
		}
		mu.Lock()
		n := len(calls)
		calls = append(calls, call)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, answer(n, call))
	}))
	t.Cleanup(server.Close)
	return server, func() []messagesCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]messagesCall(nil), calls...)
	}
}

func messagesReply(n int, stop, usage string, blocks ...string) string {
	return fmt.Sprintf(`{"id":"msg_%d","type":"message","role":"assistant","model":"claude-opus-5-5","content":[%s],"stop_reason":%q,"usage":%s}`, n, strings.Join(blocks, ","), stop, usage)
}

func toolCall(id, name string, input any) string {
	encoded, _ := json.Marshal(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
	return string(encoded)
}

// The usage of the three calls of a run with one repair: the first reads,
// the second submits a document the validator rejects, the third (the repair)
// submits a valid one.
const (
	readUsage   = `{"input_tokens":1000,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":2000,"cache_creation":{"ephemeral_5m_input_tokens":2000,"ephemeral_1h_input_tokens":0}}`
	submitUsage = `{"input_tokens":200,"output_tokens":500,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}`
	repairUsage = `{"input_tokens":1000,"output_tokens":400,"cache_read_input_tokens":0,"cache_creation_input_tokens":1500,"cache_creation":{"ephemeral_5m_input_tokens":1500,"ephemeral_1h_input_tokens":0}}`
)

// reviewScript answers a review with tool calls, a rejected document and a
// repaired one. repair, when set, answers the repair's calls instead.
func reviewScript(t *testing.T, repair func(n int, call messagesCall) string) func(int, messagesCall) string {
	return func(n int, call messagesCall) string {
		document := func(edit func(map[string]any)) string {
			encoded, err := generatetest.DocumentFor(call.bundleOf(t), func(d map[string]any) {
				// The model schema requires the estimate the stub's document omits.
				d["summary"].(map[string]any)["review_minutes"] = 5
				edit(d)
			})
			if err != nil {
				t.Fatal(err)
			}
			return string(encoded)
		}
		isRepair := strings.Contains(call.prompt, "This is validation repair")
		switch {
		case !isRepair && call.turns == 1:
			var b struct {
				Diff []struct{ Path, Change, Patch string }
			}
			_ = json.Unmarshal(call.bundleOf(t), &b)
			path := ""
			for _, hunk := range b.Diff {
				if hunk.Patch != "" && hunk.Change != "deleted" && path == "" {
					path = hunk.Path
				}
			}
			return messagesReply(n, "tool_use", readUsage,
				toolCall("toolu_read", "Read", map[string]any{"file_path": "head/" + path}),
				toolCall("toolu_glob", "Glob", map[string]any{"pattern": "**", "path": "head"}),
				toolCall("toolu_grep", "Grep", map[string]any{"pattern": ".", "path": "head"}),
				toolCall("toolu_escape", "Read", map[string]any{"file_path": "../" + filepath.Base(os.TempDir())}))
		case !isRepair:
			if len(call.last) != 4 {
				t.Errorf("tool results = %+v", call.last)
			}
			for i, result := range call.last {
				// The fourth call tried to leave the run directory.
				if result.Type != "tool_result" || result.IsError != (i == 3) || len(result.Content) == 0 {
					t.Errorf("tool result %d = %+v", i, result)
				}
			}
			// A ref to an item the section does not have: valid JSON for the
			// schema, rejected by the validator.
			return messagesReply(n, "tool_use", submitUsage, toolCall("toolu_submit", "StructuredOutput", json.RawMessage(document(func(d map[string]any) {
				section := d["sections"].([]any)[0].(map[string]any)
				section["attention"].(map[string]any)["refs"] = []any{"rsk_absent"}
			}))))
		case repair != nil:
			return repair(n, call)
		default:
			return messagesReply(n, "tool_use", repairUsage, toolCall("toolu_repair", "StructuredOutput", json.RawMessage(document(func(d map[string]any) {
				section := d["sections"].([]any)[0].(map[string]any)
				section["summary"].(map[string]any)["text"] = "The change reads " + assembletest.PlantedSecret + " from the settings."
			}))))
		}
	}
}

func anthropicOptions(t *testing.T, f *fixture, server *httptest.Server) generate.Options {
	options := f.options(t)
	options.Backend = &anthropic.Backend{APIKey: anthropicTestKey, BaseURL: server.URL}
	options.BackendVersion = ""
	options.Model = anthropic.DefaultModel
	options.Budgets = agent.Budgets{WallClock: time.Minute, MaxCalls: 10, MaxTokens: 100000, MaxProviderAttempts: 1, MaxRepairAttempts: 2}
	return options
}

func TestAnthropicBackendGeneratesARepairedRedactedDocument(t *testing.T) {
	f := newFixture(t)
	f.repo.Dirty(t)
	server, calls := messagesStub(t, reviewScript(t, nil))
	options := anthropicOptions(t, f, server)
	options.DiagnosticsOut = filepath.Join(t.TempDir(), "diagnostics.json")
	result, err := generate.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, f.progress.String())
	}
	got := calls()
	if len(got) != 3 || got[0].key != anthropicTestKey {
		t.Fatalf("%d calls", len(got))
	}
	if !strings.Contains(got[2].prompt, "rsk_absent") || !strings.Contains(got[2].prompt, "The validator found") {
		t.Errorf("the repair prompt lacks the rejected document and its findings")
	}

	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	if findings := validator.ValidateStored(result.Document, nil); len(findings) > 0 {
		t.Errorf("the stored document is invalid: %v", findings)
	}
	if strings.Contains(string(result.Document), assembletest.PlantedSecret) || !strings.Contains(string(result.Document), "The change reads") {
		t.Errorf("the free text was not masked in place")
	}
	var document struct {
		Review struct {
			Generator struct{ Model, PromptVersion string } `json:"generator"`
		} `json:"review"`
	}
	if err := json.Unmarshal(result.Document, &document); err != nil || document.Review.Generator.Model != "claude-opus-5-5" {
		t.Errorf("generator = %+v %v", document.Review.Generator, err)
	}
	if result.Metadata.Backend != "anthropic" || result.Metadata.Model != "claude-opus-5-5" || result.Metadata.Calls != 3 {
		t.Errorf("metadata = %+v", result.Metadata)
	}

	outcome := result.Outcome
	if outcome.ValidationRepairs != 1 || outcome.Calls != 3 || outcome.Tokens != 3100+700+2900 {
		t.Errorf("repairs %d calls %d tokens %d", outcome.ValidationRepairs, outcome.Calls, outcome.Tokens)
	}
	a := outcome.Accounting
	if a == nil || len(a.Attempts) != 3 || a.Usage.Unreported || a.Usage.CacheRead != 2000 || a.Usage.CacheCreate != 3500 || a.Usage.Input != 2200 || a.Usage.Output != 1000 {
		t.Fatalf("accounting = %+v", a)
	}
	// Recorded cost: each call at Claude Opus 5.5's rates (input 4, output
	// 20, cache read 0.2, 5-minute cache write 5 USD per million tokens).
	cost, reason := pricing.Embedded().AccountingCost(a)
	want := (1000*4 + 100*20 + 2000*5 + 200*4 + 500*20 + 2000*0.2 + 1000*4 + 400*20 + 1500*5) / 1e6
	if cost == nil || math.Abs(*cost-want) > 1e-9 {
		t.Errorf("cost = %v (%s), want %v", cost, reason, want)
	}

	for name, text := range map[string]string{"progress": f.progress.String(), "document": string(result.Document)} {
		if strings.Contains(text, anthropicTestKey) {
			t.Errorf("the key is in the %s", name)
		}
	}
	diagnostics, err := os.ReadFile(options.DiagnosticsOut)
	if err != nil || strings.Contains(string(diagnostics), anthropicTestKey) || strings.Contains(string(diagnostics), assembletest.PlantedSecret) {
		t.Errorf("diagnostics: %v", err)
	}
	for _, want := range []string{`"tool": "Glob"`, `"tool": "Grep"`, `"status": "invalid_path"`, `"kind": "document"`} {
		if !strings.Contains(string(diagnostics), want) {
			t.Errorf("diagnostics lack %s", want)
		}
	}
	f.requireClean(t)
}

func TestAnthropicBudgetsSpanRepairs(t *testing.T) {
	t.Run("calls", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Dirty(t)
		// The repair reads first, so it needs two calls; the run has one left.
		server, calls := messagesStub(t, reviewScript(t, func(n int, _ messagesCall) string {
			return messagesReply(n, "tool_use", smallRepairUsage, toolCall("toolu_r", "Read", map[string]any{"file_path": "bundle.json"}))
		}))
		options := anthropicOptions(t, f, server)
		options.Budgets.MaxCalls = 3
		result, err := generate.Run(context.Background(), options)
		if generate.CategoryOf(err) != generate.CategoryBudget || !strings.Contains(err.Error(), agent.GenerationBudgetExceededReason("model calls")) {
			t.Fatalf("err = %v", err)
		}
		if len(calls()) != 3 || result.Outcome.Calls != 3 || result.Document != nil {
			t.Errorf("%d calls, outcome %d", len(calls()), result.Outcome.Calls)
		}
	})
	t.Run("tokens", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Dirty(t)
		server, calls := messagesStub(t, reviewScript(t, nil))
		options := anthropicOptions(t, f, server)
		// The first pass charges 3800 tokens, which leaves the repair 1200;
		// its response charges 2900.
		options.Budgets.MaxTokens = 5000
		result, err := generate.Run(context.Background(), options)
		if generate.CategoryOf(err) != generate.CategoryBudget || !strings.Contains(err.Error(), agent.GenerationBudgetExceededReason("tokens")) {
			t.Fatalf("err = %v", err)
		}
		got := calls()
		if len(got) != 3 || got[2].maxTokens != 1200 || result.Outcome.Tokens != 6700 {
			t.Errorf("calls %d, repair max_tokens %d, tokens %d", len(got), got[len(got)-1].maxTokens, result.Outcome.Tokens)
		}
		// What the run spent is still accounted, for its cost.
		if a := result.Outcome.Accounting; a == nil || len(a.Attempts) != 3 || a.Usage.Unreported {
			t.Errorf("accounting = %+v", a)
		}
	})
	t.Run("wall clock", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Dirty(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}))
		t.Cleanup(server.Close)
		options := anthropicOptions(t, f, server)
		options.Budgets.WallClock = 300 * time.Millisecond
		_, err := generate.Run(context.Background(), options)
		if generate.CategoryOf(err) != generate.CategoryBudget || !strings.Contains(err.Error(), "wall_clock") {
			t.Fatalf("err = %v", err)
		}
	})
}

const smallRepairUsage = `{"input_tokens":10,"output_tokens":10}`
