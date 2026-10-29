//go:build !windows

package baumcli

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
)

// fakeAppDaemon answers the control requests the hooks send: it records the
// events and requests, decides every request with decision, and hands out
// message once.
type fakeAppDaemon struct {
	mu       sync.Mutex
	asks     []agentrequests.Ask
	events   []hookevents.Event
	takes    int
	decision agentrequests.Decision
	message  string
}

func startFakeAppDaemon(t *testing.T, paths daemon.Paths) *fakeAppDaemon {
	t.Helper()
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	// An interactive Claude Code session, whatever runs the tests.
	t.Setenv(claudeEntrypointEnv, "cli")
	t.Setenv(claudeAttendedEnv, "1")
	fake := &fakeAppDaemon{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go fake.serve(conn)
		}
	}()
	return fake
}

func (f *fakeAppDaemon) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var request daemon.Request
	if json.Unmarshal(line, &request) != nil {
		return
	}
	f.mu.Lock()
	var result any = struct{}{}
	switch request.Method {
	case hookevents.Method:
		var event hookevents.Event
		_ = json.Unmarshal(request.Params, &event)
		f.events = append(f.events, event)
	case agentrequests.MethodRequest:
		var ask agentrequests.Ask
		_ = json.Unmarshal(request.Params, &ask)
		f.asks = append(f.asks, ask)
		result = f.decision
	case agentrequests.MethodTakeMessage:
		f.takes++
		result = agentrequests.Taken{Text: f.message}
		f.message = ""
	}
	f.mu.Unlock()
	data, _ := json.Marshal(result)
	response, _ := json.Marshal(daemon.Response{OK: true, Result: data})
	_, _ = conn.Write(append(response, '\n'))
}

func (f *fakeAppDaemon) lastAsk(t *testing.T) agentrequests.Ask {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asks) == 0 {
		t.Fatal("the hook asked the daemon nothing")
	}
	return f.asks[len(f.asks)-1]
}

func index(i int) *int { return &i }

const (
	claudeBashRequest = `{"session_id":"s-1","hook_event_name":"PermissionRequest","permission_mode":"default","tool_name":"Bash",` +
		`"tool_input":{"command":"just platform::test ./internal/githubgit/...","description":"Run the githubgit tests","timeout":120000},` +
		`"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"just platform::test:*"}],"behavior":"allow","destination":"localSettings"},` +
		`{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`
	claudeQuestionRequest = `{"session_id":"s-1","hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[` +
		`{"question":"Which approach?","header":"Approach","multiSelect":false,"options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]},` +
		`{"question":"Which parts?","header":"Parts","multiSelect":true,"options":[{"label":"x"},{"label":"y"},{"label":"z"}]}]}}`
	codexBashRequest = `{"session_id":"c-1","turn_id":"t","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build","description":"Clean the build"}}`
)

func TestPermissionRequestPrintsTheAppsDecision(t *testing.T) {
	for name, tt := range map[string]struct {
		agent, payload string
		decision       agentrequests.Decision
		want           string
	}{
		"claude allow": {"claude-code", claudeBashRequest, agentrequests.Decision{Behavior: "allow"},
			`{"hookSpecificOutput":{"decision":{"behavior":"allow"},"hookEventName":"PermissionRequest"}}`},
		"claude deny": {"claude-code", claudeBashRequest, agentrequests.Decision{Behavior: "deny"},
			`{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"` + deniedMessage + `"},"hookEventName":"PermissionRequest"}}`},
		"claude always allow": {"claude-code", claudeBashRequest, agentrequests.Decision{Behavior: "allow", Suggestion: index(0)},
			`{"hookSpecificOutput":{"decision":{"behavior":"allow","updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"just platform::test:*"}],"behavior":"allow","destination":"localSettings"}]},"hookEventName":"PermissionRequest"}}`},
		"claude unknown suggestion": {"claude-code", claudeBashRequest, agentrequests.Decision{Behavior: "allow", Suggestion: index(2)}, ``},
		"claude question": {"claude-code", claudeQuestionRequest, agentrequests.Decision{Behavior: "allow", Answers: []agentrequests.Answer{{Options: []int{1}}, {Options: []int{0, 2}}}},
			`{"hookSpecificOutput":{"decision":{"behavior":"allow","updatedInput":{"answers":{"Which approach?":"B","Which parts?":"x, z"},"questions":[` +
				`{"question":"Which approach?","header":"Approach","multiSelect":false,"options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]},` +
				`{"question":"Which parts?","header":"Parts","multiSelect":true,"options":[{"label":"x"},{"label":"y"},{"label":"z"}]}]}},"hookEventName":"PermissionRequest"}}`},
		"claude question in the user's words": {"claude-code", claudeQuestionRequest, agentrequests.Decision{Behavior: "allow", Answers: []agentrequests.Answer{{Text: "neither"}, {Options: []int{1}}}},
			`"answers":{"Which approach?":"neither","Which parts?":"y"}`},
		"claude question unanswered": {"claude-code", claudeQuestionRequest, agentrequests.Decision{Behavior: "allow", Answers: []agentrequests.Answer{{Options: []int{1}}}}, ``},
		"claude nothing decided":     {"claude-code", claudeBashRequest, agentrequests.Decision{}, ``},
		"codex allow": {"codex", codexBashRequest, agentrequests.Decision{Behavior: "allow"},
			`{"hookSpecificOutput":{"decision":{"behavior":"allow"},"hookEventName":"PermissionRequest"}}`},
		"codex deny": {"codex", codexBashRequest, agentrequests.Decision{Behavior: "deny"},
			`{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"` + deniedMessage + `"},"hookEventName":"PermissionRequest"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			paths := useTestHookDaemon(t)
			withoutEntire(t)
			t.Chdir(t.TempDir())
			fake := startFakeAppDaemon(t, paths)
			fake.decision = tt.decision
			stdout, _, err := runCLIWithInput(t, tt.payload, "hooks", "agent", "--user", tt.agent, "permission-request")
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.want == "" && stdout != "":
				t.Fatalf("stdout = %q, want nothing", stdout)
			case strings.HasPrefix(tt.want, "{") && stdout != tt.want+"\n":
				t.Fatalf("stdout = %s\nwant     %s", stdout, tt.want)
			case !strings.Contains(stdout, tt.want):
				t.Fatalf("stdout = %s, want it to contain %s", stdout, tt.want)
			}
			fake.mu.Lock()
			events := fake.events
			fake.mu.Unlock()
			if len(events) != 1 || events[0].Name != "permission-request" {
				t.Fatalf("events = %+v", events)
			}
		})
	}
}

// The daemon is told what the app shows, never the raw payload.
func TestPermissionRequestSendsTheDaemonWhatTheAppShows(t *testing.T) {
	paths := useTestHookDaemon(t)
	withoutEntire(t)
	t.Chdir(t.TempDir())
	fake := startFakeAppDaemon(t, paths)
	start := time.Now()
	if _, _, err := runCLIWithInput(t, claudeBashRequest, "hooks", "agent", "--user", "claude-code", "permission-request"); err != nil {
		t.Fatal(err)
	}
	ask := fake.lastAsk(t)
	want := []agentrequests.Row{{Label: "Command", Value: "just platform::test ./internal/githubgit/...", Code: true}, {Label: "Description", Value: "Run the githubgit tests"},
		{Label: "Other input", Value: `{"timeout":120000}`, Code: true}}
	if ask.Agent != "claude-code" || ask.SessionID != "s-1" || ask.AgentID != "" || ask.Kind != "permission" || ask.Tool != "Bash" || len(ask.Input) != 3 ||
		ask.Input[0] != want[0] || ask.Input[1] != want[1] || ask.Input[2] != want[2] || ask.StartedAt.Before(start.Add(-time.Second)) || ask.Questions != nil {
		t.Fatalf("ask = %+v", ask)
	}
	if len(ask.Suggestions) != 2 || ask.Suggestions[0] != "Always allow Bash(just platform::test:*) in this project" || ask.Suggestions[1] != "Switch to acceptEdits mode for this session" {
		t.Fatalf("suggestions = %q", ask.Suggestions)
	}
	if ask.Digest != agentrequests.Digest("Bash", json.RawMessage(`{"timeout":120000,"description":"Run the githubgit tests","command":"just platform::test ./internal/githubgit/..."}`)) {
		t.Fatal("the digest is not the tool call's")
	}

	if _, _, err := runCLIWithInput(t, claudeQuestionRequest, "hooks", "agent", "--user", "claude-code", "permission-request"); err != nil {
		t.Fatal(err)
	}
	ask = fake.lastAsk(t)
	if ask.Kind != "question" || len(ask.Input) != 0 || len(ask.Questions) != 2 || ask.Questions[1].Question != "Which parts?" || !ask.Questions[1].MultiSelect ||
		len(ask.Questions[0].Options) != 2 || ask.Questions[0].Options[1] != (agentrequests.Option{Label: "B", Description: "second"}) {
		t.Fatalf("ask = %+v", ask)
	}

	if _, _, err := runCLIWithInput(t, codexBashRequest, "hooks", "agent", "--user", "codex", "permission-request"); err != nil {
		t.Fatal(err)
	}
	if ask = fake.lastAsk(t); ask.Agent != "codex" || len(ask.Suggestions) != 0 || len(ask.Input) != 2 || ask.Input[0].Value != "rm -rf build" {
		t.Fatalf("ask = %+v", ask)
	}
}

// With no daemon the hook records its event in the spool and prints nothing,
// at once: the agent asks in its terminal.
func TestPermissionRequestIsSilentWithoutADaemon(t *testing.T) {
	paths := useTestHookDaemon(t)
	withoutEntire(t)
	t.Chdir(t.TempDir())
	start := time.Now()
	stdout, _, err := runCLIWithInput(t, claudeBashRequest, "hooks", "agent", "--user", "claude-code", "permission-request")
	if err != nil || stdout != "" {
		t.Fatalf("hook: %q, %v", stdout, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the hook took %s", elapsed)
	}
	if events, raw := spooledHookEvents(t, paths); len(events) != 1 || events[0].Name != "permission-request" || strings.Contains(string(raw[0]), "githubgit") {
		t.Fatalf("spool = %+v", events)
	}
}

func TestStopCarriesTheQueuedMessage(t *testing.T) {
	t.Run("user-wide", func(t *testing.T) {
		paths := useTestHookDaemon(t)
		withoutEntire(t)
		t.Chdir(t.TempDir())
		fake := startFakeAppDaemon(t, paths)
		fake.message = "Also update the README."
		stdout, _, err := runCLIWithInput(t, `{"session_id":"s-1","stop_hook_active":false}`, "hooks", "agent", "--user", "claude-code", "stop")
		if err != nil {
			t.Fatal(err)
		}
		want := `{"decision":"block","reason":"` + strings.ReplaceAll(messageReason("Also update the README."), "\n", `\n`) + `"}` + "\n"
		if stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		// The message was taken: the next stop prints nothing.
		stdout, _, err = runCLIWithInput(t, `{"session_id":"s-1","stop_hook_active":true}`, "hooks", "agent", "--user", "codex", "stop")
		if err != nil || stdout != "" {
			t.Fatalf("second stop: %q, %v", stdout, err)
		}
	})
	t.Run("merged into Entire's output", func(t *testing.T) {
		paths := useTestHookDaemon(t)
		// A process Entire leaves running with its output open does not
		// hold the hook. (The test's own standard error is a pipe, which
		// would.)
		stubEntireBinary(t, "#!/bin/sh\ncat >/dev/null\nsleep 5 2>/dev/null &\necho '{\"systemMessage\":\"checkpoint saved\"}'\n")
		t.Chdir(t.TempDir())
		fake := startFakeAppDaemon(t, paths)
		fake.message = "go on"
		start := time.Now()
		stdout, _, err := runCLIWithInput(t, `{"session_id":"s-1"}`, "hooks", "agent", "claude-code", "stop")
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("the stop hook took %s", elapsed)
		}
		var out map[string]string
		if json.Unmarshal([]byte(stdout), &out) != nil || out["systemMessage"] != "checkpoint saved" || out["decision"] != "block" || out["reason"] != messageReason("go on") {
			t.Fatalf("stdout = %q", stdout)
		}
	})
	t.Run("Entire decided already", func(t *testing.T) {
		paths := useTestHookDaemon(t)
		entireOut := `{"decision":"block","reason":"entire"}`
		stubEntireBinary(t, "#!/bin/sh\ncat >/dev/null\necho '"+entireOut+"'\n")
		t.Chdir(t.TempDir())
		fake := startFakeAppDaemon(t, paths)
		fake.message = "keep me"
		stdout, _, err := runCLIWithInput(t, `{"session_id":"s-1"}`, "hooks", "agent", "claude-code", "stop")
		if err != nil || stdout != entireOut+"\n" {
			t.Fatalf("stdout = %q, %v", stdout, err)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if fake.takes != 0 || fake.message != "keep me" {
			t.Fatal("the message was taken though it could not be delivered")
		}
	})
	t.Run("nothing queued or no daemon", func(t *testing.T) {
		paths := useTestHookDaemon(t)
		withoutEntire(t)
		t.Chdir(t.TempDir())
		start := time.Now()
		if stdout, _, err := runCLIWithInput(t, `{"session_id":"s-1"}`, "hooks", "agent", "--user", "claude-code", "stop"); err != nil || stdout != "" {
			t.Fatalf("without a daemon: %q, %v", stdout, err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("the stop hook was held for %s", elapsed)
		}
		startFakeAppDaemon(t, paths)
		if stdout, _, err := runCLIWithInput(t, `{"session_id":"s-1"}`, "hooks", "agent", "--user", "claude-code", "stop"); err != nil || stdout != "" {
			t.Fatalf("with nothing queued: %q, %v", stdout, err)
		}
	})
}

// A finished or failed tool call's event carries the digest of its input,
// never the input, and the subagent it ran in.
func TestPostToolUseEventCarriesOnlyADigest(t *testing.T) {
	for _, event := range []string{"post-tool-use", "post-tool-use-failure"} {
		t.Run(event, func(t *testing.T) {
			paths := useTestHookDaemon(t)
			withoutEntire(t)
			t.Chdir(t.TempDir())
			payload := `{"session_id":"s-1","agent_id":"agent-1","tool_name":"Bash","tool_input":{"command":"cat ~/.netrc"},"tool_response":{"stdout":"` + strings.Repeat("x", 300<<10) + `"}}`
			if stdout, _, err := runCLIWithInput(t, payload, "hooks", "agent", "--user", "claude-code", event); err != nil || stdout != "" {
				t.Fatalf("hook: %q, %v", stdout, err)
			}
			events, raw := spooledHookEvents(t, paths)
			if len(events) != 1 || events[0].Session == nil || events[0].Session.AgentID != "agent-1" ||
				events[0].Session.ToolDigest != agentrequests.Digest("Bash", json.RawMessage(`{"command":"cat ~/.netrc"}`)) {
				t.Fatalf("events = %+v", events)
			}
			if strings.Contains(string(raw[0]), "netrc") {
				t.Fatalf("the event kept the tool input: %s", raw[0])
			}
		})
	}
}

// An event is stamped with when its hook started, not when its work ended.
func TestHookEventIsStampedWithTheHooksStart(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	if event := newHookEvent(hookevents.KindAgent, "claude-code", "post-tool-use", started); !event.Time.Equal(started) || event.Time.Location() != time.UTC {
		t.Fatalf("time %s, want %s", event.Time, started)
	}
}

// A Claude Code session nobody answers in a terminal (`claude -p`, the SDK)
// is not held: the hook decides nothing at once. Without either variable it
// holds as before.
func TestUnattendedClaudeSessionIsNotHeld(t *testing.T) {
	for name, tt := range map[string]struct {
		entrypoint, attended string
		asked                bool
	}{
		"interactive":   {"cli", "1", true},
		"print mode":    {"sdk-cli", "0", false},
		"sdk":           {"sdk-ts", "", false},
		"unattended":    {"", "0", false},
		"not said":      {"", "", true},
		"attended only": {"", "1", true},
	} {
		t.Run(name, func(t *testing.T) {
			paths := useTestHookDaemon(t)
			withoutEntire(t)
			t.Chdir(t.TempDir())
			fake := startFakeAppDaemon(t, paths)
			fake.decision = agentrequests.Decision{Behavior: "allow"}
			for variable, value := range map[string]string{claudeEntrypointEnv: tt.entrypoint, claudeAttendedEnv: tt.attended} {
				t.Setenv(variable, value)
				if value == "" {
					_ = os.Unsetenv(variable)
				}
			}
			stdout, _, err := runCLIWithInput(t, claudeBashRequest, "hooks", "agent", "--user", "claude-code", "permission-request")
			if err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			asked := len(fake.asks) > 0
			fake.mu.Unlock()
			if asked != tt.asked || (stdout != "") != tt.asked {
				t.Fatalf("asked %v, stdout %q", asked, stdout)
			}
		})
	}
}

func TestSubagentRequestCarriesItsAgent(t *testing.T) {
	prompt, ok := parsePermissionRequest("claude-code", []byte(`{"session_id":"s-1","agent_id":"agent-1","tool_name":"Bash","tool_input":{"command":"make"}}`))
	if !ok || prompt.ask.AgentID != "agent-1" {
		t.Fatalf("%+v %v", prompt, ok)
	}
}

func TestInputRowsShowEverythingTheApprovalCovers(t *testing.T) {
	rows := func(agent, tool, input string) []agentrequests.Row {
		var members map[string]json.RawMessage
		_ = json.Unmarshal([]byte(input), &members)
		return inputRows(agent, tool, members, json.RawMessage(input))
	}
	same := func(t *testing.T, got []agentrequests.Row, want ...agentrequests.Row) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("rows = %+v\nwant   %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	}
	same(t, rows("claude-code", "Bash", `{"command":"make","dangerouslyDisableSandbox":true,"run_in_background":true,"timeout":5}`),
		agentrequests.Row{Label: "Command", Value: "make", Code: true},
		agentrequests.Row{Label: "Sandbox", Value: "Off: runs outside the sandbox"},
		agentrequests.Row{Label: "Background", Value: "Runs in the background"},
		agentrequests.Row{Label: "Other input", Value: `{"timeout":5}`, Code: true})
	// A switch that is off changes nothing and is not shown.
	same(t, rows("claude-code", "Bash", `{"command":"make","dangerouslyDisableSandbox":false,"description":""}`),
		agentrequests.Row{Label: "Command", Value: "make", Code: true})
	same(t, rows("claude-code", "Edit", `{"file_path":"/a","old_string":"x","new_string":"y","replace_all":true}`),
		agentrequests.Row{Label: "File", Value: "/a", Code: true},
		agentrequests.Row{Label: "Replace", Value: "x", Code: true},
		agentrequests.Row{Label: "With", Value: "y", Code: true},
		agentrequests.Row{Label: "Replace all", Value: "Every occurrence"})
	same(t, rows("claude-code", "Agent", `{"description":"Look","prompt":"Find it","subagent_type":"Explore","model":"opus"}`),
		agentrequests.Row{Label: "Description", Value: "Look"},
		agentrequests.Row{Label: "Subagent", Value: "Explore", Code: true},
		agentrequests.Row{Label: "Model", Value: "opus", Code: true},
		agentrequests.Row{Label: "Prompt", Value: "Find it"})
	same(t, rows("claude-code", "NotebookEdit", `{"notebook_path":"/n.ipynb","cell_id":"c1","edit_mode":"delete","new_source":""}`),
		agentrequests.Row{Label: "Notebook", Value: "/n.ipynb", Code: true},
		agentrequests.Row{Label: "Edit mode", Value: "delete", Code: true},
		agentrequests.Row{Label: "Cell", Value: "c1", Code: true})
	same(t, rows("claude-code", "mcp__fs__write", `{"path":"/a","content":"x"}`),
		agentrequests.Row{Label: "Input", Value: `{"content":"x","path":"/a"}`, Code: true})
	same(t, rows("codex", "Bash", `{"command":["rm","-rf","build"],"sandbox_permissions":"require_escalated"}`),
		agentrequests.Row{Label: "Command", Value: "rm -rf build", Code: true},
		agentrequests.Row{Label: "Other input", Value: `{"sandbox_permissions":"require_escalated"}`, Code: true})

	var edits []string
	for i := range maxShownEdits + 2 {
		all := ""
		if i == 1 {
			all = `,"replace_all":true`
		}
		edits = append(edits, `{"old_string":"o`+strconv.Itoa(i)+`","new_string":"n`+strconv.Itoa(i)+`"`+all+`}`)
	}
	got := rows("claude-code", "MultiEdit", `{"file_path":"/a","edits":[`+strings.Join(edits, ",")+`]}`)
	if len(got) != 2+2*maxShownEdits+1 || got[1] != (agentrequests.Row{Label: "Edits", Value: strconv.Itoa(maxShownEdits + 2)}) ||
		got[2] != (agentrequests.Row{Label: "Edit 1: replace", Value: "o0", Code: true}) || got[3] != (agentrequests.Row{Label: "Edit 1: with", Value: "n0", Code: true}) ||
		got[4].Label != "Edit 2: replace every occurrence of" || got[len(got)-1].Label != "More edits" || !strings.Contains(got[len(got)-1].Value, `"o9"`) {
		t.Fatalf("MultiEdit rows = %+v", got)
	}
}

func TestSuggestionLabels(t *testing.T) {
	for raw, want := range map[string]string{
		`{"type":"addRules","rules":[{"toolName":"Read"}],"behavior":"allow","destination":"userSettings"}`:                        "Always allow Read everywhere",
		`{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"rm:*"}],"behavior":"deny","destination":"projectSettings"}`: "Always deny Bash(rm:*) in this project, for everyone",
		`{"type":"addDirectories","directories":["/tmp/a"],"destination":"session"}`:                                               "Allow access to /tmp/a for this session",
		`{"type":"somethingNew"}`: "Apply the suggested permission change",
	} {
		if got := suggestionLabel(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
