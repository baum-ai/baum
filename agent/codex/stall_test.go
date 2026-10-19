//go:build unix

package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
)

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

// Tools and turns are logged with the command and never its output.
func TestCodexLogsToolsAndTurnsWithoutOutput(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	body := `#!/bin/sh
cat > /dev/null
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc '"'"'cat main.go'"'"'","aggregated_output":"","exit_code":null,"status":"in_progress"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc '"'"'cat main.go'"'"'","aggregated_output":"FILE CONTENTS","exit_code":0,"status":"completed"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"{\"ok\":true}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":5}}'
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	run := request(t.TempDir())
	run.Logger = slog.New(slog.NewTextHandler(logs, nil))
	run.Activity = agent.NewActivity()
	if _, err := (&Backend{Binary: script, Profile: Container}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	// The finished command no longer runs: it cannot time out.
	if running := run.Activity.Running(); running != 0 {
		t.Fatalf("%d tools still running after the command finished", running)
	}
	text := logs.String()
	for _, want := range []string{
		`msg="codex turn started" turn=1`,
		`msg="codex tool started" item=item_0 tool=command_execution command="/bin/bash -lc 'cat main.go'"`,
		`msg="codex tool finished" item=item_0 tool=command_execution status=completed duration=`,
		`exit_code=0`,
		`msg="codex turn completed" turn=1 duration=`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("logs lack %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "FILE CONTENTS") || strings.Contains(text, `{\"ok\":true}`) {
		t.Fatalf("logs carry output:\n%s", text)
	}
	if long := bounded(strings.Repeat("é", 400), maxLoggedCommandBytes); len(long) > maxLoggedCommandBytes+len("…") || !strings.HasSuffix(long, "…") {
		t.Fatalf("bounded command = %d bytes", len(long))
	}
}

// A command Codex never finishes ends the attempt at the tool timeout; the run
// is retried once and then fails as stalled, with the stalled command in the logs.
func TestCodexHungCommandStallsTheRun(t *testing.T) {
	dir := t.TempDir()
	for _, root := range []string{"head", "merge-base", "home"} {
		if err := os.Mkdir(filepath.Join(dir, root), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(t.TempDir(), "codex")
	body := `#!/bin/sh
cat > /dev/null
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc '"'"'git log'"'"'","aggregated_output":"","exit_code":null,"status":"in_progress"}}'
exec sleep 60
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	fixture := &bundle.Bundle{Repository: "org/repo", PullRequest: bundle.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	generator := agent.Generator{
		Backend: &Backend{Binary: script, Profile: Container}, Validator: validator,
		Prompts: agent.StaticPrompts{"": {Version: "test-v1", Body: "Review."}}, Logger: slog.New(slog.NewTextHandler(logs, nil)),
	}
	started := time.Now()
	outcome, _, err := generator.Generate(context.Background(), agent.Run{
		Bundle: fixture, EncodedBundle: encoded, Directory: dir, Endpoint: testEndpoint,
		Budgets: agent.Budgets{WallClock: time.Minute, MaxCalls: 5, MaxTokens: 1000, MaxProviderAttempts: 1, MaxRepairAttempts: 1, ToolTimeout: 300 * time.Millisecond},
	})
	if err == nil || err.Error() != agent.ReasonStalled {
		t.Fatalf("err = %v, want %q", err, agent.ReasonStalled)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("the stalled run took %s", elapsed)
	}
	if outcome.Calls != 2 || outcome.ProviderAttempts != 2 || !outcome.Usage.Unreported {
		t.Fatalf("outcome = %+v", outcome)
	}
	text := logs.String()
	if strings.Count(text, `msg="codex tool started" item=item_0 tool=command_execution command="/bin/bash -lc 'git log'"`) != 2 ||
		strings.Count(text, `msg="review agent stalled" cause=tool_timeout tool=command_execution item=item_0`) != 2 {
		t.Fatalf("logs:\n%s", text)
	}
}
