package uiserver_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/hookevents"
)

// panelAgent is the session panel's agent in the browser fixture: a scripted
// Claude Code that runs nothing. Each turn reports its session, runs one
// tool, streams its answer a few words at a time and finishes, writing its
// transcript and hook events as Claude Code and its hooks do, so the panel
// and the Sessions page show it as they show a real session. A message that
// mentions a comment has it propose one for the person's pending review
// (propose_review_comment) through baum's MCP server, as the agent would
// call the tool, before it answers. A message that mentions the review
// prompt has it propose a change to it (propose_prompt): the repository's
// prompt on a pull request's page, the global one when it says "every
// repository". A request about the tiles' layout
// ("move the risks to the top", "make this wider", "remove this", "undo",
// "reset") runs the layout tools over HTTP as the agent CLI would (layout).
// A queue test can hold its turn open until the messages have been edited.
type panelAgent struct {
	events *hookevents.Store
	home   string
	// mcp is the server the turn's MCP token authenticates with.
	mcp *agentcontext.Server

	mu    sync.Mutex
	turns map[string][]string
	// answers are each session's answers so far, by turn.
	answers map[string][]string
	// tweaks counts the theme edits proposed, so each has its own colour.
	tweaks int
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
	answers := append([]string(nil), a.answers[id]...)
	a.mu.Unlock()
	// The prompt is in the transcript at once, its answer once the turn ends.
	writePanelTranscript(path, id, r.Directory, prompts, answers, false)
	a.hook(id, r.Directory, path, "user-prompt-submit")
	if strings.Contains(r.Prompt, "Wait for the queue.") {
		hold := filepath.Join(os.Getenv("BAUM_APP_STATE_DIR"), "hold-panel-turn")
		for {
			if _, err := os.Stat(hold); errors.Is(err, os.ErrNotExist) {
				break
			} else if err != nil {
				return agent.Result{SessionID: id}, err
			}
			select {
			case <-ctx.Done():
				return agent.Result{SessionID: id}, ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
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
	if strings.Contains(strings.ToLower(r.Prompt), "comment") {
		a.proposeComment(ctx, r)
	}
	if strings.Contains(strings.ToLower(r.Prompt), "theme") {
		a.proposeTheme(ctx, r)
	}
	if strings.Contains(strings.ToLower(r.Prompt), "review prompt") {
		a.proposePrompt(ctx, r)
	}
	answer := panelAnswer(r.Prompt)
	// A request to add a tile runs through the add tools.
	if said, ok := a.tiles(ctx, r); ok {
		answer = said
	}
	a.mu.Lock()
	if a.answers == nil {
		a.answers = map[string][]string{}
	}
	a.answers[id] = append(a.answers[id], answer)
	answers = append([]string(nil), a.answers[id]...)
	a.mu.Unlock()
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
	writePanelTranscript(path, id, r.Directory, prompts, answers, true)
	a.hook(id, r.Directory, path, "stop")
	return agent.Result{Document: []byte(answer), SessionID: id, Calls: 1, Tokens: 120}, nil
}

// answerOf is a turn's answer, or panelAnswer for one with none recorded.
func answerOf(answers []string, i int, prompt string) string {
	if i < len(answers) {
		return answers[i]
	}
	return panelAnswer(prompt)
}

// panelComment is the comment the panel agent proposes for the pending
// review.
const panelComment = "Cap the retry delay at five minutes, as docs/guide.md says."

// proposeComment proposes panelComment for the pending review of the pull
// request the panel's page shows, as the agent's propose_review_comment
// tool call would: a change the person saves. A refusal, such as from a page
// that shows no pull request, is recorded in the transcript as the tool's
// failure.
func (a *panelAgent) proposeComment(ctx context.Context, r agent.Request) {
	status, text := "completed", "Proposed the comment; the person saves it in the panel."
	err := errors.New("the turn has no MCP server")
	if a.mcp != nil && r.MCP != nil {
		consumer, ok := a.mcp.Registry.Authenticate(r.MCP.Token)
		page, open := a.mcp.Registry.Page(consumer.ID)
		switch {
		case !ok:
			err = errors.New("the turn's MCP token is not known")
		case !open || page.Page != agentcontext.PagePullRequest:
			err = agentcontext.ErrNotCurrentPage
		default:
			after, _ := json.Marshal(agentcontext.ReviewCommentChange{Body: panelComment})
			_, err = a.mcp.Proposals.Propose(ctx, consumer.ID, agentcontext.Proposal{Page: agentcontext.PagePullRequest,
				Target: agentcontext.PullRequestTarget(page.Repository, page.PullRequest), After: after})
		}
	}
	if err != nil {
		status, text = "failed", err.Error()
	}
	if r.Transcript != nil {
		r.Transcript(agent.TranscriptEntry{Kind: "tool", ID: "toolu_comment", Tool: "mcp__baum__propose_review_comment", Title: "propose_review_comment", Status: status, Text: text})
	}
}

// panelPromptLine is the line the panel agent adds to the review prompt.
const panelPromptLine = "Flag every database migration that can lose data, and say how to roll it back."

// proposePrompt proposes the shared review prompt with panelPromptLine added,
// as the agent's propose_prompt call would from whatever page is open: the
// page's repository's prompt, or the global one for "every repository" or a
// page that shows no repository.
func (a *panelAgent) proposePrompt(ctx context.Context, r agent.Request) {
	status, text := "completed", "Proposed the prompt; the person saves it in the panel."
	err := errors.New("the turn has no MCP server")
	if a.mcp != nil && r.MCP != nil {
		consumer, ok := a.mcp.Registry.Authenticate(r.MCP.Token)
		page, _ := a.mcp.Registry.Page(consumer.ID)
		if !ok {
			err = errors.New("the turn's MCP token is not known")
		} else {
			entry, _ := prompt.Lookup(prompt.IDReviewV2Shared)
			kept := strings.TrimRight(entry.Default, "\n")
			body := kept + "\n\n" + panelPromptLine + entry.Default[len(kept):]
			after, _ := json.Marshal(agentcontext.PromptChange{Text: &body})
			target := agentcontext.TargetPrompt + string(entry.ID)
			if page.Repository != "" && !strings.Contains(strings.ToLower(r.Prompt), "every repository") {
				target = agentcontext.RepositoryTarget(page.Repository, target)
			}
			_, err = a.mcp.Proposals.Propose(ctx, consumer.ID, agentcontext.Proposal{Page: agentcontext.ProposalAnywhere, Target: target, After: after})
		}
	}
	if err != nil {
		status, text = "failed", err.Error()
	}
	if r.Transcript != nil {
		r.Transcript(agent.TranscriptEntry{Kind: "tool", ID: "toolu_prompt", Tool: "mcp__baum__propose_prompt", Title: "propose_prompt", Status: status, Text: text})
	}
}

// panelTheme is the colour theme the panel agent proposes: a dark theme, so a
// browser test can see a dark base in the light slot. The proposal's name is
// the prompt's last word after "theme named", when it has one, so a later
// proposal replaces the preview with a different one.
const panelTheme = `{"name":"Dusk","base":"dark","tokens":{"--color-paper":"#e8eef4","--color-shadow":"#10202e","--brand-green-600":"#5aa9e6"}}`

// proposeTheme proposes a colour theme from whatever page is open, as the
// agent's propose_theme tool call would; the app previews it. A prompt with
// "tweak theme <id>" edits that custom theme (theme_id), with a new text colour
// each time it is asked; "as <name>" names it, else the name stays "Mine".
func (a *panelAgent) proposeTheme(ctx context.Context, r agent.Request) {
	// "switch theme to <id>" switches to an existing theme for the slot in
	// use (propose_theme_choice) instead.
	if i := strings.Index(strings.ToLower(r.Prompt), "switch theme to "); i >= 0 {
		a.proposeThemeChoice(ctx, r, strings.Fields(r.Prompt[i+len("switch theme to "):])[0])
		return
	}
	status, text := "completed", "Proposed the theme; the person saves it in the panel."
	err := errors.New("the turn has no MCP server")
	if a.mcp != nil && r.MCP != nil {
		consumer, ok := a.mcp.Registry.Authenticate(r.MCP.Token)
		if !ok {
			err = errors.New("the turn's MCP token is not known")
		} else {
			after := json.RawMessage(panelTheme)
			if i := strings.Index(strings.ToLower(r.Prompt), "theme named "); i >= 0 {
				name := strings.TrimSpace(r.Prompt[i+len("theme named "):])
				after, _ = json.Marshal(map[string]any{"name": name, "base": "light", "tokens": map[string]string{"--color-paper": "#fdf6e3"}})
			}
			if fields := strings.Fields(r.Prompt); strings.Contains(strings.ToLower(r.Prompt), "tweak theme ") {
				for i, word := range fields {
					if strings.EqualFold(word, "tweak") && i+2 < len(fields) {
						a.mu.Lock()
						a.tweaks++
						n := a.tweaks
						a.mu.Unlock()
						name := "Mine"
						if as := strings.Index(r.Prompt, " as "); as >= 0 {
							name = strings.TrimSpace(r.Prompt[as+len(" as "):])
						}
						after, _ = json.Marshal(map[string]any{"theme_id": fields[i+2], "name": name, "base": "dark",
							"tokens": map[string]string{"--color-paper": fmt.Sprintf("#e8ee%02x", n)}})
						break
					}
				}
			}
			_, err = a.mcp.Proposals.Propose(ctx, consumer.ID, agentcontext.Proposal{Page: agentcontext.ProposalAnywhere,
				Target: agentcontext.TargetTheme, After: after})
		}
	}
	if err != nil {
		status, text = "failed", err.Error()
	}
	if r.Transcript != nil {
		r.Transcript(agent.TranscriptEntry{Kind: "tool", ID: "toolu_theme", Tool: "mcp__baum__propose_theme", Title: "propose_theme", Status: status, Text: text})
	}
}

// proposeThemeChoice proposes the existing theme id for the slot in use from
// whatever page is open, as the agent's propose_theme_choice call would.
func (a *panelAgent) proposeThemeChoice(ctx context.Context, r agent.Request, id string) {
	status, text := "completed", "Proposed the theme for the slot in use; the person saves it in the panel."
	err := errors.New("the turn has no MCP server")
	if a.mcp != nil && r.MCP != nil {
		consumer, ok := a.mcp.Registry.Authenticate(r.MCP.Token)
		if !ok {
			err = errors.New("the turn's MCP token is not known")
		} else {
			after, _ := json.Marshal(map[string]string{"theme_id": id, "slot": agentcontext.SlotInUse})
			_, err = a.mcp.Proposals.Propose(ctx, consumer.ID, agentcontext.Proposal{Page: agentcontext.ProposalAnywhere,
				Target: agentcontext.TargetThemeChoice, After: after})
		}
	}
	if err != nil {
		status, text = "failed", err.Error()
	}
	if r.Transcript != nil {
		r.Transcript(agent.TranscriptEntry{Kind: "tool", ID: "toolu_theme_choice", Tool: "mcp__baum__propose_theme_choice", Title: "propose_theme_choice", Status: status, Text: text})
	}
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
// and answer are left out until answered is set. answers are the turns'
// answers, panelAnswer for a turn without one.
func writePanelTranscript(path, id, dir string, prompts, answers []string, answered bool) {
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
			"content": []any{map[string]any{"type": "text", "text": answerOf(answers, i, prompt)}}}
		write(reply)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	temporary := path + ".tmp"
	if os.WriteFile(temporary, []byte(lines.String()), 0o600) == nil {
		_ = os.Rename(temporary, path)
	}
}
