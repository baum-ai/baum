//go:build unix

package claude

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

const converseSession = "6c2a4f1e-0b7d-4c55-9a7e-2f3b1d0c8e91"

// A conversation turn loads what the person set up: none of the flags that
// confine every other run, the permission mode its access asks for, the
// session resumed on a follow-up, and baum's server beside the person's own.
func TestConverseArguments(t *testing.T) {
	config := `{"mcpServers":{"baum":{"headers":{"Authorization":"Bearer ${BAUM_MCP_TOKEN}"},"type":"http","url":"http://127.0.0.1:43123/api/mcp"}}}`
	for _, c := range []struct {
		name   string
		access agent.Access
		resume string
		mode   string
	}{
		{"auto, first turn", agent.AccessAuto, "", "auto"},
		{"auto, follow-up", agent.AccessAuto, converseSession, "auto"},
		{"full, first turn", agent.AccessFull, "", "bypassPermissions"},
		{"full, follow-up", agent.AccessFull, converseSession, "bypassPermissions"},
		{"workspace, first turn", agent.AccessWorkspace, "", "acceptEdits"},
		{"workspace, follow-up", agent.AccessWorkspace, converseSession, "acceptEdits"},
	} {
		args := (&Backend{Model: "opus"}).converseArgs(agent.Request{ResumeSession: c.resume, MCP: testMCP()}, c.access)
		want := []string{"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--model", "opus", "--permission-mode", c.mode}
		if c.resume != "" {
			want = append(want, "--resume", c.resume)
		}
		want = append(want, "--mcp-config", config, "--allowedTools", "mcp__baum")
		if !slices.Equal(args, want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, args, want)
		}
		for _, arg := range args {
			if strings.Contains(arg, mcpToken) {
				t.Errorf("%s: token in argument %q", c.name, arg)
			}
		}
	}
	// No model leaves the person's own default.
	if args := (&Backend{}).converseArgs(agent.Request{MCP: testMCP()}, agent.AccessFull); slices.Contains(args, "--model") {
		t.Errorf("an empty model passed --model: %q", args)
	}
}

// A turn reports its session as soon as the init event names it and its
// answer as it streams, keeps no review flags, passes the token only in the
// environment, and ends with the final message as its document.
func TestConverseRunsThePersonsSession(t *testing.T) {
	events := []string{
		`{"type":"system","subtype":"init","session_id":"` + converseSession + `"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"fixture","usage":{"input_tokens":3,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}`,
		`{"type":"assistant","message":{"id":"m1","model":"fixture","content":[{"type":"text","text":"Hello"}],"usage":{"input_tokens":3,"output_tokens":2}}}`,
		`{"type":"result","subtype":"success","result":"Hello","session_id":"` + converseSession + `","usage":{"input_tokens":3,"output_tokens":2}}`,
	}
	script := fakeClaude(t, "2.1.300 (Claude Code)", "cat > prompt.txt\nprintf '%s\\n' \"$@\" > arguments.txt\nenv > environment.txt\ncat <<'EVENTS'\n"+strings.Join(events, "\n")+"\nEVENTS\n")
	for _, name := range append(slices.Clone(sessionKept), sessionStripped...) {
		t.Setenv(name, "inherited")
	}
	dir := t.TempDir()
	var started, deltas, messages []string
	result, err := (&Backend{Binary: script}).Converse(context.Background(), agent.Request{
		Directory: dir, Prompt: "hi there", MCP: testMCP(),
		SessionStarted: func(id string) { started = append(started, id) },
		TextDelta:      func(text string) { deltas = append(deltas, text) },
		Transcript: func(entry agent.TranscriptEntry) {
			if entry.Kind == "message" {
				messages = append(messages, entry.Text)
			}
		},
	}, agent.AccessWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Document) != "Hello" || result.SessionID != converseSession {
		t.Fatalf("result = %q, session %q", result.Document, result.SessionID)
	}
	if !slices.Equal(started, []string{converseSession}) || !slices.Equal(deltas, []string{"Hel", "lo"}) || !slices.Equal(messages, []string{"Hello"}) {
		t.Fatalf("started %q, deltas %q, messages %q", started, deltas, messages)
	}
	if prompt := readFile(t, dir, "prompt.txt"); prompt != "hi there" {
		t.Fatalf("prompt = %q", prompt)
	}
	args := strings.Split(strings.TrimSpace(readFile(t, dir, "arguments.txt")), "\n")
	for _, flag := range []string{"--setting-sources", "--restricted", "--tools", "--strict-mcp-config", "--system-prompt", "--no-session-persistence", "--json-schema", "--settings"} {
		if slices.Contains(args, flag) {
			t.Errorf("a conversation passed %s: %q", flag, args)
		}
	}
	env := environmentOf(t, dir)
	if env[agent.MCPTokenEnv] != mcpToken {
		t.Errorf("the MCP token is not in the environment")
	}
	for _, name := range sessionKept {
		if env[name] != "inherited" {
			t.Errorf("the person's session lost %s", name)
		}
	}
	for _, name := range sessionStripped {
		if _, ok := env[name]; ok {
			t.Errorf("the person's session got %s", name)
		}
	}

	// A review's environment keeps none of what a session adds.
	_, _ = (&Backend{Binary: script}).Run(context.Background(), agent.Request{Directory: dir, Prompt: "p", Schema: []byte(`{}`)})
	env = environmentOf(t, dir)
	for _, name := range append(slices.Clone(sessionKept), sessionStripped...) {
		if _, ok := env[name]; ok {
			t.Errorf("a review got %s", name)
		}
	}
}

// sessionKept are variables a person's own session keeps and a review does
// not; sessionStripped are removed from both.
var (
	sessionKept     = []string{"SSH_AUTH_SOCK", "GH_TOKEN", "GITHUB_TOKEN", "GIT_SSH_COMMAND", "GIT_AUTHOR_NAME"}
	sessionStripped = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY"}
)

// Only the User profile runs a conversation, with a known access.
func TestConverseRefusals(t *testing.T) {
	ctx := context.Background()
	if _, err := (&Backend{Profile: Isolated}).Converse(ctx, agent.Request{}, agent.AccessFull); err == nil {
		t.Error("the Isolated profile ran a conversation")
	}
	if _, err := (&Backend{}).Converse(ctx, agent.Request{}, agent.Access("everything")); err == nil {
		t.Error("an unknown access was accepted")
	}
}
