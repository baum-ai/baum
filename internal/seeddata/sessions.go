package seeddata

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// sessionPlan is one agent session: which agent, where it worked, how many
// prompts it answered and when it started.
type sessionPlan struct {
	agent  string // "claude-code" or "codex"
	id     string
	title  string
	cwd    string
	branch string
	turns  int
	start  time.Time
	model  string
	path   string
}

// planSessions spreads the sessions over the clones. The first session of
// each agent is the long one (o.LongTranscriptTurns prompts); the others
// range from a few prompts to several hundred.
func planSessions(o Options, repos []*repoPlan, clones []string) []*sessionPlan {
	rng := rand.New(rand.NewPCG(uint64(o.Seed), 0x5e55_1015))
	var out []*sessionPlan
	add := func(agent string, n int, model string) {
		for i := 0; i < n; i++ {
			r := rng.IntN(len(repos))
			pr := repos[r].pullRequests[rng.IntN(len(repos[r].pullRequests))]
			turns := 3 + rng.IntN(60)
			switch {
			case i == 0:
				turns = o.LongTranscriptTurns
			case rng.IntN(4) == 0:
				turns = 200 + rng.IntN(600)
			}
			if i > 0 {
				turns = min(turns, o.LongTranscriptTurns-1)
			}
			out = append(out, &sessionPlan{agent: agent, id: uuid(rng), title: pr.title, cwd: clones[r], branch: pr.branch, turns: turns,
				start: o.Epoch.Add(-time.Duration(1+rng.IntN(20*24)) * time.Hour), model: model})
		}
	}
	add("claude-code", o.ClaudeSessions, "claude-opus-4-5")
	add("codex", o.CodexSessions, "gpt-5.5")
	return out
}

func uuid(rng *rand.Rand) string {
	return fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", rng.Uint32(), rng.Uint32()&0xffff, rng.Uint32()&0xfff, rng.Uint32()&0xfff, rng.Uint64()&0xffffffffffff)
}

var (
	prompts = []string{"Run the tests and fix whatever fails.", "Why does %s return early here?", "Rename %s to something clearer.",
		"Add a test for the empty %s case.", "Make %s handle a cancelled context.", "Explain how %s reaches the store.",
		"Split the %s function in two.", "Check the error handling around %s.", "Update the docs for %s."}
	commands = []string{"go test ./...", "go vet ./...", "git status --short", "rg -n %s", "git diff --stat", "go build ./..."}
)

// promptText is the person's prompt for a turn.
func promptText(rng *rand.Rand, turn int) string {
	p := prompts[rng.IntN(len(prompts))]
	if strings.Contains(p, "%s") {
		p = fmt.Sprintf(p, identifiers[rng.IntN(len(identifiers))]+functions[rng.IntN(len(functions))])
	}
	if turn%25 == 0 {
		p += "\n\nTake your time: read the callers first, and list what you change before you change it."
	}
	return p
}

// replyText is the agent's answer; every tenth is a longer one with a list
// and a code block.
func replyText(rng *rand.Rand, turn int) string {
	if turn%10 != 0 {
		return fmt.Sprintf("Done. %s now %s, and the tests pass.", identifiers[rng.IntN(len(identifiers))], []string{"checks its input", "returns the wrapped error", "reads the limit once"}[rng.IntN(3)])
	}
	var b strings.Builder
	b.WriteString("Here is what I changed:\n\n")
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&b, "- `%s%s` %s.\n", functions[rng.IntN(len(functions))], strings.ToUpper(identifiers[i][:1])+identifiers[i][1:], purpose(rng))
	}
	b.WriteString("\n```go\nif err := validate(ctx, request); err != nil {\n\treturn fmt.Errorf(\"validate: %w\", err)\n}\n```\n\nThe tests pass.")
	return b.String()
}

// writeClaudeTranscript writes a Claude Code transcript where Claude Code
// keeps it: under home's .claude/projects, in its cwd's folder.
func writeClaudeTranscript(o Options, home string, s *sessionPlan) (int64, error) {
	rng := rand.New(rand.NewPCG(uint64(o.Seed), hash(s.id)))
	var lines strings.Builder
	add := func(v map[string]any) {
		v["sessionId"], v["cwd"], v["gitBranch"], v["isSidechain"], v["version"] = s.id, s.cwd, s.branch, false, "2.1.220"
		raw, _ := json.Marshal(v)
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	title, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": s.title, "sessionId": s.id})
	lines.Write(append(title, '\n'))
	parent := any(nil)
	for i := 1; i <= s.turns; i++ {
		t := s.start.Add(time.Duration(i) * 90 * time.Second)
		at := func(d time.Duration) string { return t.Add(d).UTC().Format("2006-01-02T15:04:05.000Z") }
		n := fmt.Sprint(i)
		usage := map[string]any{"input_tokens": 1200 + rng.IntN(4000), "output_tokens": 80 + rng.IntN(900), "cache_creation_input_tokens": 0, "cache_read_input_tokens": 20000 + rng.IntN(60000)}
		add(map[string]any{"type": "user", "uuid": "u" + n, "parentUuid": parent, "timestamp": at(0), "promptId": "p" + n, "promptSource": "typed",
			"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": promptText(rng, i)}})
		command := commands[rng.IntN(len(commands))]
		if strings.Contains(command, "%s") {
			command = fmt.Sprintf(command, functions[rng.IntN(len(functions))])
		}
		add(map[string]any{"type": "assistant", "uuid": "a" + n, "parentUuid": "u" + n, "timestamp": at(20 * time.Second), "requestId": "req-a" + n,
			"message": map[string]any{"id": "msg-a" + n, "role": "assistant", "model": s.model, "stop_reason": "tool_use", "usage": usage,
				"content": []any{map[string]any{"type": "tool_use", "id": "toolu_" + n, "name": "Bash", "input": map[string]any{"command": command}}}}})
		add(map[string]any{"type": "user", "uuid": "r" + n, "parentUuid": "a" + n, "timestamp": at(40 * time.Second), "promptId": "p" + n, "sourceToolAssistantUUID": "a" + n,
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_" + n,
				"content": fmt.Sprintf("ok  \tgithub.com/example/%s\t0.%03ds", s.branch, rng.IntN(1000))}}}})
		add(map[string]any{"type": "assistant", "uuid": "b" + n, "parentUuid": "r" + n, "timestamp": at(60 * time.Second), "requestId": "req-b" + n,
			"message": map[string]any{"id": "msg-b" + n, "role": "assistant", "model": s.model, "stop_reason": "end_turn", "usage": usage,
				"content": []any{map[string]any{"type": "text", "text": replyText(rng, i)}}}})
		parent = "b" + n
	}
	s.path = filepath.Join(home, ".claude", "projects", claudecode.EncodeProjectDir(s.cwd), s.id+".jsonl")
	return int64(lines.Len()), writePrivate(s.path, lines.String())
}

// writeCodexRollout writes a Codex rollout where Codex keeps it: under
// home's .codex/sessions, by the day it started.
func writeCodexRollout(o Options, home string, s *sessionPlan) (int64, error) {
	rng := rand.New(rand.NewPCG(uint64(o.Seed), hash(s.id)))
	var lines strings.Builder
	add := func(at time.Time, kind string, payload map[string]any) {
		raw, _ := json.Marshal(map[string]any{"timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"), "type": kind, "payload": payload})
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	add(s.start, "session_meta", map[string]any{"id": s.id, "timestamp": s.start.UTC().Format(time.RFC3339), "cwd": s.cwd, "originator": "codex-tui",
		"cli_version": "0.129.0", "source": "cli", "thread_source": "user", "model_provider": "openai",
		"git": map[string]any{"branch": s.branch}})
	for i := 1; i <= s.turns; i++ {
		t := s.start.Add(time.Duration(i) * 90 * time.Second)
		turn := fmt.Sprintf("turn-%d", i)
		call := fmt.Sprintf("call_%d", i)
		reply := replyText(rng, i)
		command := commands[rng.IntN(len(commands))]
		if strings.Contains(command, "%s") {
			command = fmt.Sprintf(command, functions[rng.IntN(len(functions))])
		}
		args, _ := json.Marshal(map[string]any{"cmd": command, "workdir": s.cwd})
		add(t, "event_msg", map[string]any{"type": "user_message", "message": promptText(rng, i), "kind": nil})
		add(t, "turn_context", map[string]any{"turn_id": turn, "cwd": s.cwd, "model": s.model, "effort": "high", "summary": "none"})
		add(t, "event_msg", map[string]any{"type": "task_started", "turn_id": turn, "model_context_window": 258400})
		add(t.Add(10*time.Second), "response_item", map[string]any{"type": "function_call", "name": "exec_command", "arguments": string(args), "call_id": call})
		add(t.Add(30*time.Second), "response_item", map[string]any{"type": "function_call_output", "call_id": call, "output": "Process exited with code 0"})
		add(t.Add(50*time.Second), "response_item", map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": reply}}})
		add(t.Add(50*time.Second), "event_msg", map[string]any{"type": "agent_message", "message": reply})
		add(t.Add(55*time.Second), "event_msg", map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": reply})
	}
	day := s.start.UTC()
	s.path = filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02"),
		"rollout-"+day.Format("2006-01-02T15-04-05")+"-"+s.id+".jsonl")
	return int64(lines.Len()), writePrivate(s.path, lines.String())
}

// writeSessionEvents records each session's start and end as the agents'
// hooks would, at sessionsAt, so the Sessions page lists them as ended:
// it lists an ended session for a day after its end.
func writeSessionEvents(dir string, sessions []*sessionPlan, sessionsAt time.Time) error {
	events, err := hookevents.OpenStore(dir, nil)
	if err != nil {
		return err
	}
	defer func() { _ = events.Close() }()
	for i, s := range sessions {
		end := sessionsAt.Add(-time.Duration(i+1) * 7 * time.Minute)
		// The tracker ignores events over a day old, and a session it saw
		// only end is not listed: a long session's start is recorded at
		// most twelve hours before its end.
		start := end.Add(-min(time.Duration(s.turns)*90*time.Second, 12*time.Hour))
		for _, e := range []struct {
			name string
			at   time.Time
		}{{"session-start", start}, {"session-end", end}} {
			event := hookevents.Event{V: hookevents.Version, ID: fmt.Sprintf("seed-%s-%s", s.id, e.name), Time: e.at.UTC(),
				Kind: hookevents.KindAgent, Agent: s.agent, Name: e.name, Dir: s.cwd, Client: "seeddata",
				Session: &hookevents.Session{ID: s.id, Cwd: s.cwd, TranscriptPath: s.path}}
			if _, err := events.Add(event, hookevents.ViaSocket); err != nil {
				return err
			}
		}
	}
	return nil
}

// writePrivate creates path, refusing one that exists (fs.ErrExist).
func writePrivate(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// hash is FNV-1a, to seed a stream from a name.
func hash(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
