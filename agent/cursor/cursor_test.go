//go:build unix

package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

// help is the part of cursor-agent 2026.10.16-e3cb207's --help that a run
// relies on, as the real CLI prints it.
const help = `Usage: agent [options] [command] [prompt...]

Start the Cursor Agent

Options:
  -p, --print                  Print responses to console (for scripts or
                               non-interactive use). Has access to all tools,
                               including write and shell. (default: false)
  --output-format <format>     Output format (only works with --print): text |
                               json | stream-json (default: "text")
  --mode <mode>                Start in the given execution mode. plan:
                               read-only/planning (analyze, propose plans, no
                               edits). ask: Q&A style for explanations and
                               questions (read-only). (choices: "plan", "ask")
  --model <model>              Model to use (e.g., gpt-5, sonnet-4-thinking).
  --sandbox <mode>             Explicitly enable or disable sandbox mode
                               (overrides config) (choices: "enabled",
                               "disabled")
  --trust                      Trust the current workspace without prompting
                               (default: false)
  --workspace <path-or-name>   Workspace directory or saved workspace name to
                               use (defaults to current working directory)
`

// fakeCLI writes an executable called name into a new directory on PATH, the
// only one, which answers --help with helpText and --version with a dated
// version, and otherwise runs body.
func fakeCLI(t *testing.T, name, helpText, body string) string {
	t.Helper()
	dir := t.TempDir()
	// printf is a shell builtin, so --help and --version need nothing on PATH.
	script := "#!/bin/sh\ncase \"$1\" in\n--help) printf '%s' " + quoteShell(helpText) + "; exit 0;;\n--version) printf '%s\\n' 2026.10.16-e3cb207; exit 0;;\nesac\n" + body + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return path
}

// emit is a shell command that prints each event on a line.
func emit(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString("printf '%s\\n' '" + strings.ReplaceAll(event, "'", `'\''`) + "'\n")
	}
	return b.String()
}

const (
	initEvent   = `{"type":"system","subtype":"init","apiKeySource":"login","cwd":"/x","session_id":"s","model":"Claude 4.5 Sonnet","permissionMode":"default"}`
	resultEvent = `{"type":"result","subtype":"success","is_error":false,"duration_ms":5,"result":"ignored","session_id":"s","usage":{"inputTokens":1000,"outputTokens":300,"cacheReadTokens":2000,"cacheWriteTokens":200}}`
)

func assistant(text, modelCall string) string {
	event := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":` + quote(text) + `}]},"session_id":"s"`
	if modelCall != "" {
		event += `,"model_call_id":"` + modelCall + `"`
	}
	return event + "}"
}

func toolEvent(subtype, tool, path, modelCall string) string {
	result := ""
	if subtype == "completed" {
		result = `,"result":{"success":{}}`
	}
	return `{"type":"tool_call","subtype":"` + subtype + `","call_id":"c-` + modelCall + `","tool_call":{"` + tool + `":{"args":{"path":` + quote(path) + `}` + result + `}},"model_call_id":"` + modelCall + `","session_id":"s"}`
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func request(dir string) agent.Request {
	return agent.Request{Directory: dir, Prompt: "review the checkout", Schema: []byte(`{"type":"object"}`), MaxCalls: 10, MaxTokens: 100000}
}

func TestRunReadsTheStreamAndSendsTheSchemaInThePrompt(t *testing.T) {
	dir := t.TempDir()
	read := toolEvent("started", "readToolCall", filepath.Join(dir, "head", "main.go"), "mc-1")
	done := toolEvent("completed", "readToolCall", filepath.Join(dir, "head", "main.go"), "mc-1")
	outside := toolEvent("started", "grepToolCall", "/etc/passwd", "mc-2")
	fakeCLI(t, "cursor-agent", help, `printf '%s\n' "$@" > arguments.txt
env > environment.txt
pwd > directory.txt
cat > prompt.txt
`+emit(initEvent, assistant("Reading.", "mc-1"), read, done, outside, assistant(`{"ok":true}`, ""), resultEvent))
	t.Setenv("CURSOR_API_KEY", "key_cursor_secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("CURSOR_CONFIG_DIR", "/cursor-config")

	backend := &Backend{}
	result, err := backend.Run(context.Background(), request(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Document) != `{"ok":true}` || result.Model != "Claude 4.5 Sonnet" || result.ModelSource != "cli_reported" {
		t.Fatalf("result = %+v", result)
	}
	// Two identified model calls and the final answer.
	if result.Calls != 3 || result.Tokens != 1000+300+200 {
		t.Fatalf("calls %d tokens %d", result.Calls, result.Tokens)
	}
	if want := (agent.Usage{Input: 1000, Output: 300, CacheRead: 2000, CacheCreate: 200}); result.Usage != want {
		t.Fatalf("usage = %+v, want %+v", result.Usage, want)
	}
	var tools []string
	for _, event := range result.Events {
		if event.Kind == "tool_call" {
			tools = append(tools, event.Tool+":"+event.Path)
		}
	}
	if !slices.Equal(tools, []string{"readToolCall:" + filepath.Join("head", "main.go"), "grepToolCall:<outside-run>"}) {
		t.Errorf("tool events = %v", tools)
	}

	args, _ := os.ReadFile(filepath.Join(dir, "arguments.txt"))
	want := strings.Join([]string{"--print", "--output-format", "stream-json", "--mode", "ask", "--sandbox", "enabled", "--workspace", dir, "--trust"}, "\n") + "\n"
	if string(args) != want {
		t.Errorf("arguments:\n%s\nwant:\n%s", args, want)
	}
	directory, _ := os.ReadFile(filepath.Join(dir, "directory.txt"))
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(directory)))
	if want, _ := filepath.EvalSymlinks(dir); got != want {
		t.Errorf("working directory = %s, want %s", got, want)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if string(prompt) != "review the checkout"+schemaHeading+`{"type":"object"}`+"\n" {
		t.Errorf("prompt on stdin = %q", prompt)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "environment.txt"))
	for _, secret := range []string{"key_cursor_secret", "sk-ant-secret", "ghp_secret"} {
		if strings.Contains(string(env), secret) {
			t.Errorf("the child environment holds %s", secret)
		}
	}
	for _, kept := range []string{"CURSOR_CONFIG_DIR=/cursor-config", "NO_OPEN_BROWSER=1"} {
		if !strings.Contains(string(env), kept) {
			t.Errorf("the child environment lacks %s", kept)
		}
	}
}

func TestRunPassesTheConfiguredModel(t *testing.T) {
	dir := t.TempDir()
	fakeCLI(t, "cursor-agent", help, `printf '%s\n' "$@" > arguments.txt
cat > /dev/null
`+emit(initEvent, assistant(`{}`, ""), resultEvent))
	result, err := (&Backend{Model: "gpt-5"}).Run(context.Background(), request(dir))
	if err != nil || result.Model != "gpt-5" || result.ModelSource != "configured" || result.Calls != 1 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if args, _ := os.ReadFile(filepath.Join(dir, "arguments.txt")); !strings.HasSuffix(string(args), "--trust\n--model\ngpt-5\n") {
		t.Errorf("arguments:\n%s", args)
	}
}

func TestRunWithoutUsageMarksItUnreported(t *testing.T) {
	fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, assistant(`{}`, ""), `{"type":"result","subtype":"success","is_error":false,"result":"{}","session_id":"s"}`))
	result, err := (&Backend{}).Run(context.Background(), request(t.TempDir()))
	if err != nil || !result.Usage.Unreported || result.Tokens != 0 || result.Calls != 1 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	// The loop's accounting keeps the run unknown rather than free.
	var accounting agent.Accounting
	accounting.Add(result, false)
	if !accounting.Usage.Unreported {
		t.Error("accounting reads a run without usage as free")
	}
}

func TestRunReturnsMalformedOutputForTheValidator(t *testing.T) {
	for name, tc := range map[string]struct{ final, want string }{
		"fenced":            {"```json\n{\"a\":1}\n```", `{"a":1}`},
		"prose around":      {"Here is the review:\n{\"a\":1}\nDone.", `{"a":1}`},
		"not JSON":          {"I could not finish the review.", "I could not finish the review."},
		"truncated":         {`{"a":`, `{"a":`},
		"schema-invalid":    {`{"unexpected":true}`, `{"unexpected":true}`},
		"result text only":  {"", `{"from":"result"}`},
		"surrounding space": {"  {\"a\":1}\n\n", `{"a":1}`},
	} {
		t.Run(name, func(t *testing.T) {
			events := []string{initEvent}
			if tc.final != "" {
				events = append(events, assistant(tc.final, ""))
			}
			// The result repeats all the text the model wrote; with no final
			// message it is all there is.
			text := tc.final
			if text == "" {
				text = `{"from":"result"}`
			}
			events = append(events, `{"type":"result","subtype":"success","is_error":false,"result":`+quote("Reading the files."+text)+`,"session_id":"s"}`)
			fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(events...))
			result, err := (&Backend{}).Run(context.Background(), request(t.TempDir()))
			if err != nil || string(result.Document) != tc.want {
				t.Fatalf("document = %q, err = %v", result.Document, err)
			}
		})
	}
	t.Run("split across events", func(t *testing.T) {
		// Text before the last tool call is not part of the answer; the final
		// turn's events are joined.
		fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, assistant("Let me look.", "mc-1"),
			toolEvent("started", "readToolCall", "a", "mc-1"), toolEvent("completed", "readToolCall", "a", "mc-1"),
			assistant(`{"summary":{"text":"one `, ""), assistant(`and two"},`, ""), assistant(`"sections":[]}`, ""),
			`{"type":"result","subtype":"success","is_error":false,"result":"x","session_id":"s"}`))
		result, err := (&Backend{}).Run(context.Background(), request(t.TempDir()))
		if err != nil || string(result.Document) != `{"summary":{"text":"one and two"},"sections":[]}` || result.Calls != 2 {
			t.Fatalf("document = %q, calls = %d, err = %v", result.Document, result.Calls, err)
		}
	})
	t.Run("final message unreadable, result text holds the document", func(t *testing.T) {
		fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, assistant("Done, see above.", ""),
			`{"type":"result","subtype":"success","is_error":false,"result":"{\"a\":1}","session_id":"s"}`))
		result, err := (&Backend{}).Run(context.Background(), request(t.TempDir()))
		if err != nil || string(result.Document) != `{"a":1}` {
			t.Fatalf("document = %q, err = %v", result.Document, err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, `{"type":"result","subtype":"success","is_error":false,"result":"  ","session_id":"s"}`))
		if _, err := (&Backend{}).Run(context.Background(), request(t.TempDir())); err == nil || !strings.Contains(err.Error(), "no review document") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no result", func(t *testing.T) {
		fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, assistant(`{}`, "")))
		if _, err := (&Backend{}).Run(context.Background(), request(t.TempDir())); err == nil || !strings.Contains(err.Error(), "without a result") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunStopsOnAToolThatDoesNotRead(t *testing.T) {
	for _, tool := range []string{"shellToolCall", "editToolCall", "deleteToolCall", "mcpToolCall", "webFetchToolCall", "taskToolCall", "somethingNewToolCall"} {
		t.Run(tool, func(t *testing.T) {
			dir := t.TempDir()
			// The fake would keep running; the backend must not wait for it.
			fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, toolEvent("started", tool, filepath.Join(dir, "x"), "mc-1"))+"sleep 30\n"+emit(resultEvent))
			started := time.Now()
			result, err := (&Backend{}).Run(context.Background(), request(dir))
			if err == nil || !strings.Contains(err.Error(), tool) || !strings.Contains(err.Error(), "does not allow") {
				t.Fatalf("err = %v", err)
			}
			if elapsed := time.Since(started); elapsed > 15*time.Second {
				t.Fatalf("the run was not stopped (%v)", elapsed)
			}
			if len(result.Document) != 0 {
				t.Error("a stopped run returned a document")
			}
			if status := agent.LastErrorStatus(result.Events); status != "disallowed_tool" {
				t.Errorf("error status = %q", status)
			}
			var rejected *agent.RejectedError
			if !errors.As(err, &rejected) || rejected.Status != "disallowed_tool" || strings.Contains(err.Error(), "model provider") {
				t.Errorf("a stopped run must not be retried: %v", err)
			}
		})
	}
}

// Detection fails closed: a tool event baum cannot read stops the run like a
// disallowed tool.
func TestRunStopsOnAToolEventItCannotRead(t *testing.T) {
	for name, line := range map[string]string{
		"tool_call of the wrong type":     `{"type":"tool_call","subtype":"started","call_id":"c","tool_call":"shellToolCall","session_id":"s"}`,
		"two tools in one call":           `{"type":"tool_call","subtype":"started","call_id":"c","tool_call":{"readToolCall":{},"shellToolCall":{}},"session_id":"s"}`,
		"tool call under another type":    `{"type":"tool_use","name":"shell","session_id":"s"}`,
		"tool_call field on another type": `{"type":"assistant","tool_call":{"shellToolCall":{}},"session_id":"s"}`,
		"a line that is not JSON":         `tool_call: shellToolCall`,
		"a completed shell call":          toolEvent("completed", "shellToolCall", "x", "mc-1"),
	} {
		t.Run(name, func(t *testing.T) {
			fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, line)+"sleep 30\n"+emit(assistant(`{}`, ""), resultEvent))
			result, err := (&Backend{}).Run(context.Background(), request(t.TempDir()))
			var rejected *agent.RejectedError
			if !errors.As(err, &rejected) || rejected.Status != "disallowed_tool" || len(result.Document) != 0 {
				t.Fatalf("err = %v, document = %q", err, result.Document)
			}
		})
	}
	t.Run("other unreadable output is not a tool event", func(t *testing.T) {
		fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, "Warning: something", `{"type":"thinking","subtype":`, assistant(`{}`, ""), resultEvent))
		if _, err := (&Backend{}).Run(context.Background(), request(t.TempDir())); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunBudgets(t *testing.T) {
	t.Run("calls", func(t *testing.T) {
		dir := t.TempDir()
		fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent,
			toolEvent("started", "readToolCall", "a", "mc-1"), toolEvent("started", "readToolCall", "b", "mc-2"))+"sleep 30\n")
		req := request(dir)
		req.MaxCalls = 1
		if _, err := (&Backend{}).Run(context.Background(), req); err == nil || err.Error() != agent.GenerationBudgetExceededReason("model calls") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("tokens", func(t *testing.T) {
		fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent, assistant(`{}`, ""), resultEvent))
		req := request(t.TempDir())
		req.MaxTokens = 1499
		result, err := (&Backend{}).Run(context.Background(), req)
		if err == nil || err.Error() != agent.GenerationBudgetExceededReason("tokens") || result.Tokens != 1500 {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
	t.Run("nothing left", func(t *testing.T) {
		req := request(t.TempDir())
		req.MaxCalls = 0
		if _, err := (&Backend{}).Run(context.Background(), req); err == nil || err.Error() != agent.GenerationBudgetExceededReason("model calls") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunCancellation(t *testing.T) {
	fakeCLI(t, "cursor-agent", help, "cat > /dev/null\n"+emit(initEvent)+"sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (&Backend{}).Run(ctx, request(t.TempDir()))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestRunFailures(t *testing.T) {
	for name, tc := range map[string]struct{ stderr, want, status string }{
		"unknown option": {"error: unknown option '--mode'", "upgrade it with `cursor-agent update`", "unsupported_option"},
		"sandbox":        {"Error: Sandbox mode is enabled but not available on this system. Sandbox requires kernel v6.2 or later.", "only with its sandbox enabled", "sandbox_unavailable"},
		"login":          {"Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable.", "run cursor-agent login", "authentication_error"},
		"other":          {"Error: something broke", "something broke", "error"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeCLI(t, "cursor-agent", help, "cat > /dev/null\necho "+quoteShell(tc.stderr)+" >&2\nexit 1\n")
			result, err := (&Backend{}).Run(context.Background(), request(t.TempDir()))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if status := agent.LastErrorStatus(result.Events); status != tc.status {
				t.Errorf("error status = %q, want %q", status, tc.status)
			}
			// The failures a retry cannot change are rejections the loop does
			// not retry; an unrecognised one may be transient. They name
			// cursor-agent, not the model provider, as the source.
			var rejected *agent.RejectedError
			if errors.As(err, &rejected) != (tc.status != "error") || (rejected != nil && rejected.Status != tc.status) {
				t.Errorf("rejected = %v for %s", rejected != nil, tc.status)
			}
			if rejected != nil && (!strings.HasPrefix(err.Error(), "cursor-agent cannot run: ") || strings.Contains(err.Error(), "model provider")) {
				t.Errorf("message = %q", err)
			}
		})
	}
	t.Run("endpoint", func(t *testing.T) {
		req := request(t.TempDir())
		req.Endpoint = &agent.Endpoint{BaseURL: "https://proxy.example", Key: "k"}
		if _, err := (&Backend{Binary: "/nonexistent"}).Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "endpoints are not supported") {
			t.Fatalf("err = %v", err)
		}
	})
}

func quoteShell(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestCheck(t *testing.T) {
	t.Run("cursor-agent on PATH", func(t *testing.T) {
		path := fakeCLI(t, "cursor-agent", help, "exit 1")
		backend := &Backend{}
		version, err := backend.Check(context.Background())
		if err != nil || version != "2026.10.16" || backend.Path() != path {
			t.Fatalf("version %q path %q err %v", version, backend.Path(), err)
		}
	})
	t.Run("agent, when it is the Cursor Agent", func(t *testing.T) {
		path := fakeCLI(t, "agent", help, "exit 1")
		t.Setenv("PATH", filepath.Dir(path))
		backend := &Backend{}
		if _, err := backend.Check(context.Background()); err != nil || backend.Path() != path {
			t.Fatalf("path %q err %v", backend.Path(), err)
		}
	})
	t.Run("agent that is another tool", func(t *testing.T) {
		path := fakeCLI(t, "agent", "Usage: agent [flags]\n  some other agent --print --mode\n", "exit 1")
		t.Setenv("PATH", filepath.Dir(path))
		if _, err := (&Backend{}).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "finding cursor-agent") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not installed", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := (&Backend{}).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "cursor-agent login") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an older CLI without ask mode or the sandbox flag", func(t *testing.T) {
		older := strings.NewReplacer(`(choices: "plan", "ask")`, `(choices: "plan")`, "--sandbox <mode>", "--isolate <mode>").Replace(help)
		fakeCLI(t, "cursor-agent", older, "exit 1")
		_, err := (&Backend{}).Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "(ask, --sandbox)") || !strings.Contains(err.Error(), "cursor-agent update") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an explicit binary that is not the Cursor Agent", func(t *testing.T) {
		path := fakeCLI(t, "something", "Usage: something\n", "exit 1")
		if _, err := (&Backend{Binary: path}).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "does not say it is the Cursor Agent") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a failed check is repeated", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		backend := &Backend{}
		if _, err := backend.Check(context.Background()); err == nil {
			t.Fatal("want an error")
		}
		fakeCLI(t, "cursor-agent", help, "exit 1")
		if _, err := backend.Check(context.Background()); err != nil {
			t.Fatalf("after installing: %v", err)
		}
	})
}

func TestExtractDocument(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:                          `{"a":1}`,
		"```\n{\"a\":1}\n```":              `{"a":1}`,
		"```json\n{\"a\":1}\n```":          `{"a":1}`,
		"text {\"a\":{\"b\":2}} more text": `{"a":{"b":2}}`,
		"two {\"a\":1} objects {\"b\":2}":  "two {\"a\":1} objects {\"b\":2}",
		"":                                 "",
		"null":                             "null",
		"```json\n{\"a\":\n```":            "```json\n{\"a\":\n```",
	} {
		if got := string(extractDocument([]byte(in))); got != want {
			t.Errorf("extractDocument(%q) = %q, want %q", in, got, want)
		}
	}
}
