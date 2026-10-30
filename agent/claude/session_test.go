//go:build unix

package claude

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

const testSession = "01a18588-ef8c-7962-b6af-d86e6966dacd"

// A run without a call budget passes no --max-turns: its wall clock bounds it.
func TestMaxTurnsOnlyWithACallBudget(t *testing.T) {
	backend := &Backend{Model: "sonnet"}
	schema := []byte(`{"type":"object"}`)
	for _, args := range [][]string{backend.args(agent.Request{Schema: schema}), backend.fixArgs(agent.Request{})} {
		if slices.Contains(args, "--max-turns") {
			t.Errorf("unbounded run has --max-turns: %q", args)
		}
	}
	for _, args := range [][]string{backend.args(agent.Request{MaxCalls: 7, Schema: schema}), backend.fixArgs(agent.Request{MaxCalls: 7})} {
		if i := slices.Index(args, "--max-turns"); i < 0 || args[i+1] != "7" {
			t.Errorf("bounded run lacks --max-turns 7: %q", args)
		}
	}
}

func TestSessionArguments(t *testing.T) {
	backend := &Backend{Model: "sonnet"}
	schema := []byte(`{"type":"object"}`)
	base := backend.args(agent.Request{MaxCalls: 7, Schema: schema})
	if !slices.Contains(base, "--no-session-persistence") {
		t.Fatalf("default args lost --no-session-persistence: %q", base)
	}
	flagsOf := func(args []string) []string {
		var flags []string
		for _, a := range args {
			if strings.HasPrefix(a, "--") {
				flags = append(flags, a)
			}
		}
		return flags
	}
	cases := []struct {
		name    string
		request agent.Request
		persist bool
		resume  string
		fork    bool
	}{
		{"persist", agent.Request{PersistSession: true}, true, "", false},
		{"resume", agent.Request{ResumeSession: testSession}, true, testSession, false},
		{"fork", agent.Request{ResumeSession: testSession, ForkSession: true}, true, testSession, true},
	}
	for _, c := range cases {
		c.request.MaxCalls, c.request.Schema = 7, schema
		got := backend.args(c.request)
		if slices.Contains(got, "--no-session-persistence") != !c.persist {
			t.Errorf("%s: --no-session-persistence presence wrong: %q", c.name, got)
		}
		i := slices.Index(got, "--resume")
		if (i >= 0) != (c.resume != "") || (i >= 0 && got[i+1] != c.resume) {
			t.Errorf("%s: --resume wrong: %q", c.name, got)
		}
		if slices.Contains(got, "--fork-session") != c.fork {
			t.Errorf("%s: --fork-session wrong: %q", c.name, got)
		}
		// Every other flag is the same as without session options.
		other := slices.Clone(got)
		if i >= 0 {
			other = slices.Delete(other, i, i+2)
		}
		other = slices.DeleteFunc(other, func(a string) bool { return a == "--fork-session" })
		want := slices.DeleteFunc(slices.Clone(base), func(a string) bool { return a == "--no-session-persistence" })
		if !slices.Equal(other, want) {
			t.Errorf("%s: other flags changed\n got %q\nwant %q", c.name, flagsOf(other), flagsOf(want))
		}
	}
	fork := backend.args(agent.Request{MaxCalls: 7, Schema: schema, ResumeSession: testSession, ForkSession: true})
	i := slices.Index(fork, "--resume")
	if fork[i+1] != testSession || fork[i+2] != "--fork-session" {
		t.Errorf("fork args = %q", fork)
	}
}

const initThenResult = `{"type":"system","subtype":"init","session_id":"init-id"}
{"type":"assistant","message":{"id":"m1","model":"fixture","usage":{"input_tokens":1,"output_tokens":1}}}
{"type":"result","subtype":"success","session_id":"result-id","structured_output":{"ok":true},"usage":{"input_tokens":1,"output_tokens":1}}`

const resultOnly = `{"type":"assistant","message":{"id":"m1","model":"fixture","usage":{"input_tokens":1,"output_tokens":1}}}
{"type":"result","subtype":"success","session_id":"result-id","structured_output":{"ok":true},"usage":{"input_tokens":1,"output_tokens":1}}`

func TestSessionIDParsing(t *testing.T) {
	for _, c := range []struct{ name, events, want string }{
		{"init wins over result", initThenResult, "init-id"},
		{"result only", resultOnly, "result-id"},
		{"absent", successEvents, ""},
	} {
		script := fakeClaude(t, "2.1.296", "cat >/dev/null\ncat <<'EVENTS'\n"+c.events+"\nEVENTS\n")
		got, err := (&Backend{Binary: script}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Prompt: "p", Schema: []byte(`{}`), MaxCalls: 2, MaxTokens: 100, PersistSession: true})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.SessionID != c.want {
			t.Errorf("%s: SessionID = %q, want %q", c.name, got.SessionID, c.want)
		}
	}
}

func TestSessionRunPassesFlagsAndKeepsEnvironment(t *testing.T) {
	script := fakeClaude(t, "2.1.296", succeeding("printf '%s\\n' \"$@\" > arguments.txt\nenv > environment.txt"))
	plain := t.TempDir()
	if _, err := (&Backend{Binary: script}).Run(context.Background(), request(plain, nil)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r := request(dir, nil)
	r.ResumeSession, r.ForkSession = testSession, true
	if _, err := (&Backend{Binary: script}).Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// The working directory is the only thing that may differ.
	want, got := environmentOf(t, plain), environmentOf(t, dir)
	for _, env := range []map[string]string{want, got} {
		delete(env, "PWD")
		delete(env, "OLDPWD")
		delete(env, "_")
	}
	if !maps.Equal(want, got) {
		t.Errorf("environment changed\n got %v\nwant %v", got, want)
	}
	args := strings.Split(strings.TrimSpace(readTestFile(t, dir, "arguments.txt")), "\n")
	if slices.Contains(args, "--no-session-persistence") || !slices.Contains(args, "--resume") || !slices.Contains(args, "--fork-session") || !slices.Contains(args, testSession) || !slices.Contains(args, "--restricted") {
		t.Errorf("args = %q", args)
	}
}

func readTestFile(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestSessionOptionsAreRefused(t *testing.T) {
	script := fakeClaude(t, "2.1.296", succeeding("touch ran"))
	for _, c := range []struct {
		name    string
		backend *Backend
		request agent.Request
		fix     bool
		want    string
	}{
		{"isolated persist", &Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}, agent.Request{PersistSession: true}, false, "only a User profile"},
		{"isolated resume", &Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}, agent.Request{ResumeSession: testSession}, false, "only a User profile"},
		{"fork without resume", &Backend{Binary: script}, agent.Request{ForkSession: true}, false, "fork needs a session"},
		{"leading dash", &Backend{Binary: script}, agent.Request{ResumeSession: "--help"}, false, "must be a UUID"},
		{"name, not UUID", &Backend{Binary: script}, agent.Request{ResumeSession: "my-session"}, false, "must be a UUID"},
		{"fix", &Backend{Binary: script}, agent.Request{PersistSession: true}, true, "only a User profile"},
	} {
		dir := t.TempDir()
		c.request.Directory, c.request.Prompt, c.request.Schema, c.request.MaxCalls, c.request.MaxTokens = dir, "p", []byte(`{}`), 2, 100
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
