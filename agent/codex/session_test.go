//go:build unix

package codex

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// threadScript records the arguments after the stdin is drained, then emits
// events.
func threadScript(events string) string {
	return "cat >/dev/null\nprintf '%s\\n' \"$@\" > arguments.txt\nenv > environment.txt\ncat <<'EVENTS'\n" + events + "\nEVENTS\n"
}

const testSession = "01a18588-ef8c-7962-b6af-d86e6966dacd"

func TestSessionArgumentsAndThreadID(t *testing.T) {
	withThread := `{"type":"thread.started","thread_id":"thread-new"}
` + successEvents
	tail := []string{"--ignore-user-config", "--ignore-rules", "--disable", "hooks", "--disable", "apps", "--disable", "multi_agent"}
	cases := []struct {
		name    string
		request agent.Request
		prefix  []string
		sandbox []string
	}{
		{"ephemeral default", agent.Request{}, []string{"exec", "--json", "--ephemeral"}, []string{"--sandbox", "read-only"}},
		{"persist", agent.Request{PersistSession: true}, []string{"exec", "--json"}, []string{"--sandbox", "read-only"}},
		{"resume", agent.Request{ResumeSession: testSession}, []string{"exec", "resume", "--json"}, []string{"-c", `sandbox_mode="read-only"`}},
		{"fork", agent.Request{ResumeSession: testSession, ForkSession: true}, []string{"exec", "fork", "--json"}, []string{"-c", `sandbox_mode="read-only"`}},
	}
	var defaultEnv map[string]string
	for _, c := range cases {
		dir := t.TempDir()
		r := schemaRequest(dir, nil)
		r.PersistSession, r.ResumeSession, r.ForkSession = c.request.PersistSession, c.request.ResumeSession, c.request.ForkSession
		script := fakeCodex(t, "codex-cli 0.165.0", threadScript(withThread))
		got, err := (&Backend{Binary: script, Model: "m"}).Run(context.Background(), r)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.SessionID != "thread-new" {
			t.Errorf("%s: SessionID = %q", c.name, got.SessionID)
		}
		args := readLines(t, dir, "arguments.txt")
		want := slices.Concat(c.prefix, tail, c.sandbox, []string{"-c", `approval_policy="never"`, "-c", `web_search="disabled"`, "--skip-git-repo-check", "--model", "m"})
		if len(args) < len(want)+2 {
			t.Fatalf("%s: too few args: %q", c.name, args)
		}
		if !slices.Equal(args[:len(want)], want) {
			t.Errorf("%s: args\n got %q\nwant prefix %q", c.name, args, want)
		}
		env := environmentOf(t, dir)
		delete(env, "PWD")
		delete(env, "OLDPWD")
		delete(env, "_")
		if c.name == "ephemeral default" {
			defaultEnv = env
		} else if !maps.Equal(env, defaultEnv) {
			t.Errorf("%s: environment differs from the default run\n got %v\nwant %v", c.name, env, defaultEnv)
		}
		rest := args[len(want):]
		if !slices.Contains(rest, "--output-schema") {
			t.Errorf("%s: no --output-schema: %q", c.name, rest)
		}
		end := rest[len(rest)-1:]
		if c.request.ResumeSession != "" {
			end = rest[len(rest)-2:]
			if !slices.Equal(end, []string{testSession, "-"}) {
				t.Errorf("%s: session id must precede the stdin marker: %q", c.name, rest)
			}
		} else if end[0] != "-" {
			t.Errorf("%s: last arg %q", c.name, end)
		}
		if c.name != "ephemeral default" && slices.Contains(args, "--ephemeral") {
			t.Errorf("%s: kept session still passes --ephemeral", c.name)
		}
	}
}

func TestSessionUnknownOptionDoesNotBlameTheVersionFloor(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", "cat >/dev/null\necho \"error: unexpected argument '--output-schema' found\" >&2\nexit 2\n")
	r := schemaRequest(t.TempDir(), nil)
	r.ResumeSession, r.ForkSession = testSession, true
	_, err := (&Backend{Binary: script}).Run(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "does not support exec resume/fork") || strings.Contains(err.Error(), minimumVersion) {
		t.Fatalf("error %v", err)
	}
}

func TestSessionIDAbsent(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.165.0", threadScript(successEvents))
	got, err := (&Backend{Binary: script}).Run(context.Background(), schemaRequest(t.TempDir(), nil))
	if err != nil || got.SessionID != "" {
		t.Fatalf("SessionID %q, err %v", got.SessionID, err)
	}
}

func TestSessionOptionsAreRefused(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.165.0", "cat >/dev/null\ntouch ran\n")
	endpoint := &agent.Endpoint{Key: "k"}
	for _, c := range []struct {
		name    string
		backend *Backend
		request agent.Request
		fix     bool
		want    string
	}{
		{"container persist", &Backend{Binary: script, Profile: Container}, agent.Request{PersistSession: true, Endpoint: endpoint}, false, "only a Local profile"},
		{"container resume", &Backend{Binary: script, Profile: Container}, agent.Request{ResumeSession: testSession, Endpoint: endpoint}, false, "only a Local profile"},
		{"fork without resume", &Backend{Binary: script}, agent.Request{ForkSession: true}, false, "fork needs a session"},
		{"leading dash", &Backend{Binary: script}, agent.Request{ResumeSession: "--last"}, false, "must be a UUID"},
		{"name, not UUID", &Backend{Binary: script}, agent.Request{ResumeSession: "my-thread"}, false, "must be a UUID"},
		{"fix", &Backend{Binary: script}, agent.Request{PersistSession: true}, true, "only a Local profile"},
	} {
		dir := t.TempDir()
		c.request.Directory, c.request.Prompt, c.request.MaxCalls, c.request.MaxTokens = dir, "p", 2, 100
		var err error
		if c.fix {
			_, err = c.backend.Fix(context.Background(), c.request)
		} else {
			_, err = c.backend.Run(context.Background(), c.request)
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "ran")); statErr == nil {
			t.Errorf("%s: the process ran", c.name)
		}
	}
}
