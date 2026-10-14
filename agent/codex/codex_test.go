//go:build unix

package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

var testEndpoint = &agent.Endpoint{BaseURL: "https://proxy.example/openai", Key: "test-key"}

// request is a run the Container profile accepts.
func request(dir string) agent.Request {
	return agent.Request{Directory: dir, Prompt: "p", MaxCalls: 2, MaxTokens: 100, Endpoint: testEndpoint}
}

func checkNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCodexProcessOutputUsageAndEnvironment(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	body := `#!/bin/sh
printf '%s\n' "$@" > arguments.txt
env > environment.txt
cat > prompt.txt
test -d "$CODEX_HOME" || { echo 'CODEX_HOME does not exist' >&2; exit 1; }
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"error","message":"Reconnecting... 2/5"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"ok\":true}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":20,"output_tokens":10}}'
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "private database")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "private github")
	dir := t.TempDir()
	backend := &Backend{Binary: script, Model: "test-model", Profile: Container}
	result, err := backend.Run(context.Background(), agent.Request{Directory: dir, Prompt: "review the checkout", Schema: []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`), MaxCalls: 2, MaxTokens: 100,
		Endpoint: &agent.Endpoint{BaseURL: "https://proxy.example/openai", Key: "test-key"}})
	if err != nil || string(result.Document) != `{"ok":true}` || result.Model != "test-model" || result.Calls != 1 || result.Tokens != 70 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Events) != 2 || result.Events[0].CacheRead != 40 || result.Events[0].CacheCreate != 20 {
		t.Fatalf("events=%+v", result.Events)
	}
	// Codex's input_tokens includes the 40 cached and 20 cache-write tokens.
	if want := (agent.Usage{Input: 40, Output: 10, CacheRead: 40, CacheCreate: 20}); result.Usage != want {
		t.Fatalf("usage = %+v, want %+v", result.Usage, want)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "arguments.txt"))
	for _, flag := range []string{"exec\n", "--json\n", "--ephemeral\n", "--sandbox\ndanger-full-access\n", "-c\nweb_search=\"disabled\"\n", "--skip-git-repo-check\n", "--model\ntest-model\n", "--output-schema\n", "-c\nopenai_base_url=\"https://proxy.example/openai\"\n"} {
		if !bytes.Contains(args, []byte(flag)) {
			t.Fatalf("missing %q in %s", flag, args)
		}
	}
	schema, err := os.ReadFile(filepath.Join(dir, "home", ".codex", "output-schema.json"))
	if err != nil || !bytes.Contains(schema, []byte(`"additionalProperties":false`)) {
		t.Fatalf("output schema = %s, %v", schema, err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "environment.txt"))
	if !bytes.Contains(env, []byte("CODEX_API_KEY=test-key")) || bytes.Contains(env, []byte("DATABASE_URL")) || bytes.Contains(env, []byte("GITHUB_APP_PRIVATE_KEY")) {
		t.Fatalf("incorrect child environment: %s", env)
	}
	if prompt, _ := os.ReadFile(filepath.Join(dir, "prompt.txt")); string(prompt) != "review the checkout" {
		t.Fatalf("prompt on stdin = %q", prompt)
	}
	_, err = backend.Run(context.Background(), agent.Request{Directory: dir, Prompt: "review the checkout", MaxCalls: 2, MaxTokens: 69, Endpoint: testEndpoint})
	if err == nil || err.Error() != agent.GenerationBudgetExceededReason("tokens") {
		t.Fatalf("token budget: %v", err)
	}
}

// escapedDescendant returns a shell command that starts a new session, writes
// its PID to pidFile and holds the inherited stdout open, so it survives the
// process-group kill.
func escapedDescendant(t *testing.T, pidFile string) string {
	t.Helper()
	t.Cleanup(func() {
		if pid, err := os.ReadFile(pidFile); err == nil {
			_ = exec.Command("kill", "-9", strings.TrimSpace(string(pid))).Run()
		}
	})
	child := "sh -c 'echo $$ > " + pidFile + "; exec sleep 60'"
	if _, err := exec.LookPath("setsid"); err == nil {
		return "setsid " + child
	}
	if _, err := exec.LookPath("perl"); err == nil {
		return "perl -MPOSIX -e 'POSIX::setsid(); exec @ARGV' " + child
	}
	t.Skip("setsid or perl is required")
	return ""
}

// A command that escapes Codex's process group keeps stdout open after the
// group kill; cancellation still returns instead of waiting for it.
func TestCodexCancellationDoesNotWaitForEscapedDescendants(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	body := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\n" + escapedDescendant(t, filepath.Join(dir, "descendant.pid")) + " &\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (&Backend{Binary: script, Profile: Container}).Run(ctx, request(t.TempDir()))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("cancellation waited %s for an escaped descendant", elapsed)
	}
}

// Errors Run detects itself stop the process the same way, without draining
// the stdout an escaped descendant still holds.
func TestCodexInternalStopDoesNotWaitForEscapedDescendants(t *testing.T) {
	for name, test := range map[string]struct{ event, want string }{
		"turn failed":   {`{"type":"turn.failed"}`, agent.ReasonProviderUnavailable},
		"parse failure": {"not json", "invalid character"},
		"token budget":  {`{"type":"turn.completed","usage":{"input_tokens":1000,"output_tokens":1}}`, agent.GenerationBudgetExceededReason("tokens")},
	} {
		// Every script is written before any parallel subtest starts a
		// process. A fork while another subtest still held its script open for
		// writing handed the child that descriptor, and exec of the script then
		// failed with "text file busy".
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "descendant.pid")
		script := filepath.Join(dir, "codex")
		body := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\n" + escapedDescendant(t, pidFile) + " &\n" +
			"while [ ! -s " + pidFile + " ]; do sleep 0.05; done\nprintf '%s\\n' '" + test.event + "'\nwait\n"
		if err := os.WriteFile(script, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			started := time.Now()
			_, err := (&Backend{Binary: script, Profile: Container}).Run(context.Background(), request(t.TempDir()))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if elapsed := time.Since(started); elapsed > 20*time.Second {
				t.Fatalf("internal stop waited %s for an escaped descendant", elapsed)
			}
		})
	}
}

func TestCodexFailureDoesNotExposeOutput(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' 'private repository text' >&2\nprintf '%s\\n' '{\"type\":\"turn.failed\",\"error\":{\"message\":\"private repository text\"}}'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := (&Backend{Binary: script, Profile: Container}).Run(context.Background(), request(t.TempDir()))
	if err == nil || strings.Contains(err.Error(), "private") || result.Calls != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if events := fmt.Sprintf("%+v", result.Events); strings.Contains(events, "private") {
		t.Fatalf("events expose the failure message: %s", events)
	}
}

// A failed turn keeps the classification of its message, so a request the
// provider rejects is told apart from an outage in the logs.
func TestCodexFailedTurnRecordsErrorStatus(t *testing.T) {
	message := `unexpected status 400 Bad Request: {\"error\":{\"message\":\"Invalid schema for response_format 'codex_output_schema'\",\"code\":\"invalid_json_schema\"}}`
	script := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"turn.failed\",\"error\":{\"message\":\""+message+"\"}}'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := (&Backend{Binary: script, Profile: Container}).Run(context.Background(), request(t.TempDir()))
	if err == nil || err.Error() != agent.ReasonProviderUnavailable {
		t.Fatalf("err = %v, want %q", err, agent.ReasonProviderUnavailable)
	}
	if status := agent.LastErrorStatus(result.Events); status != "invalid_schema" {
		t.Fatalf("status = %q, want invalid_schema (events %+v)", status, result.Events)
	}
}

// A failed turn without a message, or a nonzero exit without a failed turn,
// is classified by the last "error" event; recovered errors record nothing.
func TestCodexClassifiesLastErrorEventWithoutTurnMessage(t *testing.T) {
	for _, tt := range []struct {
		name, events, want string
		exit               bool
	}{
		{"failed turn message wins", `{"type":"error","message":"stream disconnected, retrying 1/5"}
{"type":"turn.failed","error":{"message":"unexpected status 400: invalid_json_schema"}}`, "invalid_schema", false},
		{"failed turn without message", `{"type":"error","message":"unexpected status 429 Too Many Requests"}
{"type":"turn.failed"}`, "rate_limit", false},
		{"nonzero exit", `{"type":"error","message":"unexpected status 401 Unauthorized"}`, "authentication_error", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "codex")
			body := "#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n" + tt.events + "\nEVENTS\n"
			if tt.exit {
				body += "exit 1\n"
			}
			checkNoError(t, os.WriteFile(script, []byte(body), 0700))
			result, err := (&Backend{Binary: script, Profile: Container}).Run(context.Background(), request(t.TempDir()))
			if err == nil || (!tt.exit && err.Error() != agent.ReasonProviderUnavailable) {
				t.Fatalf("err = %v", err)
			}
			if status := agent.LastErrorStatus(result.Events); status != tt.want || strings.Contains(fmt.Sprintf("%+v", result.Events), "unexpected") {
				t.Fatalf("status = %q, want %q (events %+v)", status, tt.want, result.Events)
			}
		})
	}
}
