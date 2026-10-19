package generatetest

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCursorAgentEnv names the config file of a fake cursor-agent run. The
// fake's script sets it: the backend passes the fake only an allowlisted
// environment.
const fakeCursorAgentEnv = "BAUM_FAKE_CURSOR_AGENT"

// FakeCursorAgentVersion is what the fake's --version prints.
const FakeCursorAgentVersion = "2026.10.16-e3cb207"

// FakeCursorAgentHelp is the part of cursor-agent 2026.10.16's --help a run
// relies on.
const FakeCursorAgentHelp = `Usage: agent [options] [command] [prompt...]

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

// FakeCursorPass scripts one run of the fake: by default it reports one read
// tool call and answers with a valid document for the run's bundle, with usage.
type FakeCursorPass struct {
	// AttentionRefs replaces the section's attention refs; a ref to an item
	// the section does not have makes the validator reject the document.
	AttentionRefs []string `json:"attention_refs,omitempty"`
	// SectionSummary replaces the section summary's text.
	SectionSummary string `json:"section_summary,omitempty"`
	// Fence wraps the document in prose and a Markdown fence.
	Fence bool `json:"fence,omitempty"`
	// Text, when set, is the final message instead of a document.
	Text string `json:"text,omitempty"`
	// Tool is the tool call it reports; empty is readToolCall.
	Tool string `json:"tool,omitempty"`
	// NoUsage leaves the usage out of the result.
	NoUsage bool `json:"no_usage,omitempty"`
	// Stderr, when set, is printed to stderr instead of any output, and the
	// run exits with status 1.
	Stderr string `json:"stderr,omitempty"`
	// Split sends the final message as this many assistant events, as the CLI
	// may.
	Split int `json:"split,omitempty"`
}

// FakeCursorRun is what the fake recorded of one run.
type FakeCursorRun struct {
	Args      []string `json:"args"`
	Env       []string `json:"env"`
	Directory string   `json:"directory"`
	Prompt    string   `json:"prompt"`
	// Root names the entries of the workspace root, the directory --trust
	// covers, when the run started.
	Root []string `json:"root"`
}

type fakeCursorConfig struct {
	Record string           `json:"record"`
	Passes []FakeCursorPass `json:"passes"`
}

// InstallFakeCursorAgent writes an executable named cursor-agent, in a new
// directory it returns, that re-runs this test binary as a fake cursor-agent.
// The test binary must have a test named helper that calls
// RunFakeCursorAgent. Run n follows passes[n], or the last pass after them.
// The returned function reads the runs recorded so far.
func InstallFakeCursorAgent(t testing.TB, helper string, passes ...FakeCursorPass) (dir string, runs func() []FakeCursorRun) {
	t.Helper()
	dir = t.TempDir()
	if len(passes) == 0 {
		passes = []FakeCursorPass{{}}
	}
	config := fakeCursorConfig{Record: filepath.Join(t.TempDir(), "runs.jsonl"), Passes: passes}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\n%s=%s\nexport %s\nexec %s -test.run='^%s$' -- \"$@\"\n",
		fakeCursorAgentEnv, shellQuote(configPath), fakeCursorAgentEnv, shellQuote(os.Args[0]), helper)
	if err := os.WriteFile(filepath.Join(dir, "cursor-agent"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, func() []FakeCursorRun {
		raw, _ := os.ReadFile(config.Record)
		var recorded []FakeCursorRun
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var run FakeCursorRun
			if line != "" && json.Unmarshal([]byte(line), &run) == nil {
				recorded = append(recorded, run)
			}
		}
		return recorded
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// RunFakeCursorAgent is the body of the helper test InstallFakeCursorAgent
// names. In a normal test run it does nothing; run by the fake's script it acts
// as cursor-agent and exits the process.
func RunFakeCursorAgent(t *testing.T) {
	path := os.Getenv(fakeCursorAgentEnv)
	if path == "" {
		return
	}
	os.Exit(fakeCursorAgent(path, flag.Args(), os.Stdin, os.Stdout, os.Stderr))
}

func fakeCursorAgent(configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	var config fakeCursorConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = io.WriteString(stdout, FakeCursorAgentHelp)
		return 0
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		_, _ = fmt.Fprintln(stdout, FakeCursorAgentVersion)
		return 0
	}
	prompt, _ := io.ReadAll(stdin)
	directory, _ := os.Getwd()
	previous, _ := os.ReadFile(config.Record)
	n := strings.Count(string(previous), "\n")
	var root []string
	if entries, err := os.ReadDir(directory); err == nil {
		for _, entry := range entries {
			root = append(root, entry.Name())
		}
	}
	record, _ := json.Marshal(FakeCursorRun{Args: args, Env: os.Environ(), Directory: directory, Prompt: string(prompt), Root: root})
	file, err := os.OpenFile(config.Record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	_, _ = file.Write(append(record, '\n'))
	_ = file.Close()
	pass := config.Passes[min(n, len(config.Passes)-1)]
	if pass.Stderr != "" {
		_, _ = fmt.Fprintln(stderr, pass.Stderr)
		return 1
	}

	text := pass.Text
	if text == "" {
		document, err := Document(directory, func(d map[string]any) {
			d["summary"].(map[string]any)["review_minutes"] = 4
			section := d["sections"].([]any)[0].(map[string]any)
			if pass.AttentionRefs != nil {
				refs := make([]any, len(pass.AttentionRefs))
				for i, ref := range pass.AttentionRefs {
					refs[i] = ref
				}
				section["attention"].(map[string]any)["refs"] = refs
			}
			if pass.SectionSummary != "" {
				section["summary"].(map[string]any)["text"] = pass.SectionSummary
			}
		})
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		text = string(document)
		if pass.Fence {
			text = "Here is the review.\n```json\n" + text + "\n```"
		}
	}
	tool := pass.Tool
	if tool == "" {
		tool = "readToolCall"
	}
	emit := func(event map[string]any) {
		line, _ := json.Marshal(event)
		_, _ = stdout.Write(append(line, '\n'))
	}
	session := fmt.Sprintf("session-%d", n)
	emit(map[string]any{"type": "system", "subtype": "init", "apiKeySource": "login", "cwd": directory, "session_id": session, "model": "Fake Sonnet", "permissionMode": "default"})
	emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "(prompt)"}}}, "session_id": session})
	emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Reading the changed file."}}}, "session_id": session, "model_call_id": "mc-1"})
	call := map[string]any{tool: map[string]any{"args": map[string]any{"path": filepath.Join(directory, "bundle.json")}}}
	emit(map[string]any{"type": "tool_call", "subtype": "started", "call_id": "call-1", "tool_call": call, "model_call_id": "mc-1", "session_id": session})
	call[tool].(map[string]any)["result"] = map[string]any{"success": map[string]any{}}
	emit(map[string]any{"type": "tool_call", "subtype": "completed", "call_id": "call-1", "tool_call": call, "model_call_id": "mc-1", "session_id": session})
	parts, runes := max(pass.Split, 1), []rune(text)
	for i := range parts {
		part := string(runes[i*len(runes)/parts : (i+1)*len(runes)/parts])
		emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": part}}}, "session_id": session})
	}
	result := map[string]any{"type": "result", "subtype": "success", "is_error": false, "duration_ms": 10, "duration_api_ms": 10, "result": "Reading the changed file." + text, "session_id": session}
	if !pass.NoUsage {
		result["usage"] = map[string]any{"inputTokens": 1000, "outputTokens": 300, "cacheReadTokens": 2000, "cacheWriteTokens": 200}
	}
	emit(result)
	return 0
}
