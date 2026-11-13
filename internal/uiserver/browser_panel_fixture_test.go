package uiserver_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/hookevents"
)

// panelAgent is the session panel's agent in the browser fixture: a scripted
// Claude Code that runs nothing. Each turn reports its session, runs one
// tool, streams its answer a few words at a time and finishes, writing its
// transcript and hook events as Claude Code and its hooks do, so the panel
// and the Sessions page show it as they show a real session.
type panelAgent struct {
	events *hookevents.Store
	home   string

	mu    sync.Mutex
	turns map[string][]string
}

// panelStep is how long each of a turn's steps takes: long enough for the
// browser test to see the tool running and the answer growing.
const panelStep = time.Second

// panelAnswer is the agent's answer to a message.
func panelAnswer(prompt string) string {
	return "I read `docs/guide.md` on this branch: the retry delay is capped, so a long outage cannot push retries out forever.\n\nYou asked: " + prompt
}

func (a *panelAgent) Converse(ctx context.Context, r agent.Request, _ agent.Access) (agent.Result, error) {
	id := r.ResumeSession
	if id == "" {
		id = newSessionID()
	}
	if r.SessionStarted != nil {
		r.SessionStarted(id)
	}
	path := filepath.Join(a.home, ".claude", "projects", "-panel", id+".jsonl")
	a.mu.Lock()
	if a.turns == nil {
		a.turns = map[string][]string{}
	}
	a.turns[id] = append(a.turns[id], r.Prompt)
	prompts := append([]string(nil), a.turns[id]...)
	a.mu.Unlock()
	// The prompt is in the transcript at once, its answer once the turn ends.
	writePanelTranscript(path, id, r.Directory, prompts, false)
	a.hook(id, r.Directory, path, "user-prompt-submit")
	wait := func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(panelStep):
			return nil
		}
	}
	if r.Transcript != nil {
		r.Transcript(agent.TranscriptEntry{Kind: "tool", ID: "toolu_read", Tool: "Read", Title: "Read docs/guide.md", Status: "running"})
	}
	if err := wait(); err != nil {
		return agent.Result{SessionID: id}, err
	}
	if r.Transcript != nil {
		r.Transcript(agent.TranscriptEntry{Kind: "tool", ID: "toolu_read", Tool: "Read", Title: "Read docs/guide.md", Status: "completed"})
	}
	answer := panelAnswer(r.Prompt)
	words := strings.SplitAfter(answer, " ")
	for i, part := range [][]string{words[:len(words)/3], words[len(words)/3 : 2*len(words)/3], words[2*len(words)/3:]} {
		if r.TextDelta != nil {
			r.TextDelta(strings.Join(part, ""))
		}
		if i < 2 {
			if err := wait(); err != nil {
				return agent.Result{SessionID: id}, err
			}
		}
	}
	writePanelTranscript(path, id, r.Directory, prompts, true)
	a.hook(id, r.Directory, path, "stop")
	return agent.Result{Document: []byte(answer), SessionID: id, Calls: 1, Tokens: 120}, nil
}

// hook records a Claude Code hook event of the session, as its hooks would.
func (a *panelAgent) hook(id, dir, transcript, name string) {
	e := hookevents.New(hookevents.KindAgent, "claude-code", name, dir, "fixture")
	e.Session = &hookevents.Session{ID: id, Cwd: dir, TranscriptPath: transcript}
	_, _ = a.events.Add(e, hookevents.ViaSocket)
}

// newSessionID is a random canonical UUID, as Claude Code names a session.
func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// writePanelTranscript replaces the Claude Code transcript at path with one
// turn per prompt, in the lines Claude Code writes: the prompt, a Read of
// docs/guide.md and its result, then the answer. The last turn's tool call
// and answer are left out until answered is set.
func writePanelTranscript(path, id, dir string, prompts []string, answered bool) {
	var lines strings.Builder
	write := func(v map[string]any) {
		line, _ := json.Marshal(v)
		lines.Write(line)
		lines.WriteByte('\n')
	}
	start := time.Now().UTC().Add(-time.Duration(len(prompts)) * time.Minute).Truncate(time.Second)
	usage := map[string]any{"input_tokens": 1200, "output_tokens": 80}
	for i, prompt := range prompts {
		at := func(seconds int) string {
			return start.Add(time.Duration(i)*time.Minute + time.Duration(seconds)*time.Second).Format(time.RFC3339Nano)
		}
		base := func(kind, uuid, parent string, seconds int) map[string]any {
			v := map[string]any{"isSidechain": false, "cwd": dir, "type": kind, "uuid": uuid, "sessionId": id, "timestamp": at(seconds)}
			if parent == "" {
				v["parentUuid"] = nil
			} else {
				v["parentUuid"] = parent
			}
			return v
		}
		user := base("user", fmt.Sprintf("u-%d", i), "", 0)
		user["promptId"] = fmt.Sprintf("p-%d", i)
		user["message"] = map[string]any{"role": "user", "content": prompt}
		write(user)
		if i == len(prompts)-1 && !answered {
			break
		}
		tool := fmt.Sprintf("toolu_%d", i)
		call := base("assistant", fmt.Sprintf("a-%d-tool", i), fmt.Sprintf("u-%d", i), 5)
		call["requestId"] = fmt.Sprintf("req-%d-tool", i)
		call["message"] = map[string]any{"id": fmt.Sprintf("msg-%d-tool", i), "role": "assistant", "model": "claude-opus-5", "stop_reason": "tool_use", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": tool, "name": "Read", "input": map[string]any{"file_path": filepath.Join(dir, "docs/guide.md")}}}}
		write(call)
		result := base("user", fmt.Sprintf("r-%d", i), fmt.Sprintf("a-%d-tool", i), 6)
		result["message"] = map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": tool, "content": "# Guide\n\nRetries back off, capped at five minutes."}}}
		write(result)
		reply := base("assistant", fmt.Sprintf("a-%d", i), fmt.Sprintf("r-%d", i), 20)
		reply["requestId"] = fmt.Sprintf("req-%d", i)
		reply["message"] = map[string]any{"id": fmt.Sprintf("msg-%d", i), "role": "assistant", "model": "claude-opus-5", "stop_reason": "end_turn", "usage": usage,
			"content": []any{map[string]any{"type": "text", "text": panelAnswer(prompt)}}}
		write(reply)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	temporary := path + ".tmp"
	if os.WriteFile(temporary, []byte(lines.String()), 0o600) == nil {
		_ = os.Rename(temporary, path)
	}
}
