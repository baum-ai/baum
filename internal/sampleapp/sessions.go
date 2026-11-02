package sampleapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// agentSession is one of the person's Claude Code sessions on the sample
// project: what was asked, where it worked and what it decided. live is
// how the Sessions page sees it now: "running", "idle", "ended" or "" for
// one that ended long ago.
type agentSession struct {
	id, title, branch string
	pr                int
	ago               time.Duration
	model             string
	live              string
	prompts           []string
	decided           []decisions.Decision
}

func decision(id, title, summary, rationale string, category decisions.Category, who decisions.Who, turn int, files ...string) decisions.Decision {
	return decisions.Decision{ID: id, Title: title, Summary: summary, Rationale: rationale, Category: category, Who: who, Turns: []int{turn}, Files: files}
}

var agentSessions = []agentSession{
	{id: "5a3c1e70-0000-4000-8000-000000000001", title: "Sort notes by last edit", branch: sortByEdit.branch, pr: sortByEdit.number,
		ago: 16 * time.Minute, model: "claude-sonnet-4-5", live: "running",
		prompts: []string{"Sort the notes list by when each note was last edited, newest first.",
			"Notes that were never edited should use the time they were created.", "Run the tests."},
		decided: []decisions.Decision{
			decision("dec-edited", "Sort by last edit, falling back to creation", "The list orders notes by their last edit, or by creation for notes never edited.",
				"A note never edited was last touched when it was written.", decisions.CategoryBehavior, decisions.WhoHuman, 2, "store.go", "note.go"),
			decision("dec-stable", "Keep notes with equal times in the order they were added", "Use a stable sort so notes saved in the same second do not swap places.",
				"", decisions.CategoryImplementation, decisions.WhoAgent, 3, "store.go"),
		}},
	{id: "5a3c1e70-0000-4000-8000-000000000002", title: "Explain search in the README", branch: readme.branch, pr: readme.number,
		ago: 50 * time.Minute, model: "claude-sonnet-4-5", live: "idle",
		prompts: []string{"Add a short section to the README on how search matches."},
		decided: []decisions.Decision{
			decision("dec-readme", "Keep the README to one example per feature", "Each feature gets one short example rather than a full reference.",
				"The library is small enough to read.", decisions.CategoryScope, decisions.WhoApproved, 1, "README.md"),
		}},
	{id: "5a3c1e70-0000-4000-8000-000000000003", title: "Match search without accents", branch: accents.branch, pr: accents.number,
		ago: 3 * time.Hour, model: "claude-opus-4-1", live: "ended",
		prompts: []string{"Make search ignore accents, so cafe finds Café.",
			"Use a small table of letters instead of a Unicode library.", "Add a test for it."},
		decided: []decisions.Decision{
			{ID: "dec-table", Title: "Fold accents with a small table", Summary: "Search maps accented Latin letters to plain ones with a fixed table.",
				Rationale: "It covers the notes people write without adding a dependency.", RejectedAlternative: "Unicode normalisation with golang.org/x/text",
				Category: decisions.CategoryDependency, Who: decisions.WhoHuman, Turns: []int{2}, Files: []string{"search.go"}},
			decision("dec-fold", "Ignore case and accents in one step", "One fold function lowercases and strips accents, for the query and the notes alike.",
				"", decisions.CategoryImplementation, decisions.WhoAgent, 1, "search.go"),
		}},
	{id: "5a3c1e70-0000-4000-8000-000000000004", title: "Plan tags for notes", branch: "main", ago: 2*day + 3*time.Hour, model: "claude-sonnet-4-5",
		prompts: []string{"How could notes have tags? Sketch the smallest change.", "Clean tags when a note is saved rather than when it is read."},
		decided: []decisions.Decision{
			decision("dec-clean", "Clean tags when a note is saved", "Tags are trimmed and lowercased once, on Add, so every read sees clean tags.",
				"Reads then never need to clean anything.", decisions.CategoryArchitecture, decisions.WhoHuman, 2, "store.go"),
		}},
	{id: "5a3c1e70-0000-4000-8000-000000000005", title: "Tidy the search code", branch: "main", ago: 4*day + 2*time.Hour, model: "claude-sonnet-4-5",
		prompts: []string{"Tidy search.go without changing what it does.", "Run the tests."}},
	{id: "5a3c1e70-0000-4000-8000-000000000006", title: "Speed up the store tests", branch: "main", ago: 6*day + 5*time.Hour, model: "claude-haiku-4-5",
		prompts: []string{"The store tests are slow. Find out why.", "Share one store between the read-only tests."}},
	{id: "5a3c1e70-0000-4000-8000-000000000007", title: "List the newest notes first", branch: newestFirst.branch, pr: newestFirst.number,
		ago: 9*day + 2*time.Hour, model: "claude-sonnet-4-5",
		prompts: []string{"List notes newest first.", "Sort a copy so callers cannot reorder the store."},
		decided: []decisions.Decision{
			decision("dec-newest", "Show the newest notes first", "The list starts with the note written last.",
				"People mostly look for what they wrote last.", decisions.CategoryInterface, decisions.WhoHuman, 1, "store.go"),
		}},
}

// cwd is where a session worked: its branch's worktree, else the clone.
func (s agentSession) cwd(p project) string {
	if dir := p.worktrees[s.branch]; dir != "" {
		return dir
	}
	return p.dir
}

// turnTimes are when each of the session's prompts was sent.
func (s agentSession) turnTimes(now time.Time) map[int]time.Time {
	times := map[int]time.Time{}
	for i := range s.prompts {
		times[i+1] = now.Add(-s.ago + time.Duration(i)*6*time.Minute)
	}
	return times
}

// writeSessions writes the sessions' transcripts where Claude Code keeps
// them under home, their hook events, the decisions capture would have read
// from them and its ledger, and a plan usage history.
func writeSessions(ctx context.Context, p project, home, appDB string, events *hookevents.Store, decided *decisionlog.Store, now time.Time) error {
	for _, s := range agentSessions {
		path, err := writeTranscript(home, s, p, now)
		if err != nil {
			return err
		}
		if s.live != "" {
			names := map[string]string{"running": "user-prompt-submit", "idle": "stop", "ended": "session-end"}
			start := hookevents.New(hookevents.KindAgent, "claude-code", "session-start", s.cwd(p), "sample")
			last := hookevents.New(hookevents.KindAgent, "claude-code", names[s.live], s.cwd(p), "sample")
			times := s.turnTimes(now)
			start.Time, last.Time = times[1].UTC(), times[len(s.prompts)].Add(2*time.Minute).UTC()
			for _, e := range []hookevents.Event{start, last} {
				e.Session = &hookevents.Session{ID: s.id, Cwd: s.cwd(p), TranscriptPath: path}
				e.Terminal = &hookevents.Terminal{TTY: "/dev/ttys004"}
				if _, err := events.Add(e, hookevents.ViaSocket); err != nil {
					return err
				}
			}
		}
		if len(s.decided) == 0 {
			continue
		}
		if err := decided.Save(ctx, decisionlog.Extraction{Meta: decisionlog.Meta{Agent: "claude-code", ID: s.id, Repository: repository, Branch: s.branch, Path: path, Fingerprint: "sample"},
			ContentHash: "sample-" + s.id, Decisions: s.decided, TurnTimes: s.turnTimes(now)}); err != nil {
			return err
		}
		attempt, err := decided.Begin(ctx, decisionlog.Attempt{Agent: "claude-code", ID: s.id, Repository: repository, Backend: "claude", Model: "claude-haiku-4-5", Mode: "full", Reserved: 0.05})
		if err == nil {
			cost := 0.004 + 0.002*float64(len(s.prompts))
			err = decided.Settle(ctx, attempt, decisionlog.AttemptSucceeded, "", &cost, decisions.Usage{})
		}
		if err != nil {
			return err
		}
	}
	var samples []planusage.Sample
	for i := range 16 {
		used := float64((i*23)%90 + 8)
		samples = append(samples, planusage.Sample{At: now.Add(time.Duration(i-16) * 3 * time.Hour).UTC(), Provider: "claude", Window: "5-hour", UsedPercent: used})
	}
	for i, used := range []float64{18, 29, 37, 46} {
		samples = append(samples, planusage.Sample{At: now.Add(time.Duration(i-4) * 12 * time.Hour).UTC(), Provider: "claude", Window: "weekly", UsedPercent: used})
	}
	raw, err := json.Marshal(samples)
	if err != nil {
		return err
	}
	return appdb.Write(appDB, "state", "plan-usage-history", raw)
}

// writeTranscript writes a Claude Code transcript of the session: its
// title, then each prompt answered after running the tests once. A session
// on a pull request edits its files first and commits them last, so the
// pull request's page links it.
func writeTranscript(home string, s agentSession, p project, now time.Time) (string, error) {
	cwd := s.cwd(p)
	pr, onPull := pullNumbered(s.pr)
	var lines strings.Builder
	add := func(v map[string]any) {
		v["sessionId"], v["cwd"], v["gitBranch"], v["isSidechain"], v["version"] = s.id, cwd, s.branch, false, "2.1.220"
		raw, _ := json.Marshal(v)
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	title, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": s.title, "sessionId": s.id})
	lines.Write(append(title, '\n'))
	times := s.turnTimes(now)
	parent := any(nil)
	for i, prompt := range s.prompts {
		at := func(d time.Duration) string { return times[i+1].Add(d).UTC().Format("2006-01-02T15:04:05.000Z") }
		n := fmt.Sprintf("%d", i+1)
		usage := map[string]any{"input_tokens": 1800 + 400*i, "output_tokens": 350 + 90*i, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 12000 + 3000*i}
		add(map[string]any{"type": "user", "uuid": "u" + n, "parentUuid": parent, "timestamp": at(0), "promptId": "p" + n, "promptSource": "typed", "origin": map[string]any{"kind": "human"},
			"message": map[string]any{"role": "user", "content": prompt}})
		uses := []any{map[string]any{"type": "tool_use", "id": "tool-" + n, "name": "Bash", "input": map[string]any{"command": "go test ./..."}}}
		results := []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-" + n, "content": "ok  \texample.com/notes\t0.012s"}}
		if onPull && i == 0 {
			var edits, edited []any
			for j, file := range pullFiles(pr) {
				tool := fmt.Sprintf("edit-%s-%d", n, j)
				edits = append(edits, map[string]any{"type": "tool_use", "id": tool, "name": "Edit",
					"input": map[string]any{"file_path": filepath.Join(cwd, file), "old_string": "", "new_string": pullText(pr, file)}})
				edited = append(edited, map[string]any{"type": "tool_result", "tool_use_id": tool, "content": "The file " + filepath.Join(cwd, file) + " has been updated."})
			}
			uses, results = append(edits, uses...), append(edited, results...)
		}
		add(map[string]any{"type": "assistant", "uuid": "a" + n, "parentUuid": "u" + n, "timestamp": at(time.Minute), "requestId": "req-a" + n,
			"message": map[string]any{"id": "msg-a" + n, "role": "assistant", "model": s.model, "stop_reason": "tool_use", "usage": usage, "content": uses}})
		add(map[string]any{"type": "user", "uuid": "r" + n, "parentUuid": "a" + n, "timestamp": at(2 * time.Minute), "promptId": "p" + n, "sourceToolAssistantUUID": "a" + n,
			"message": map[string]any{"role": "user", "content": results}})
		last, answer := "r"+n, reply(prompt)
		if onPull && i == len(s.prompts)-1 {
			head, message := p.heads[pr.number], pr.commits[len(pr.commits)-1].message
			add(map[string]any{"type": "assistant", "uuid": "c" + n, "parentUuid": last, "timestamp": at(2*time.Minute + 30*time.Second), "requestId": "req-c" + n,
				"message": map[string]any{"id": "msg-c" + n, "role": "assistant", "model": s.model, "stop_reason": "tool_use", "usage": usage,
					"content": []any{map[string]any{"type": "tool_use", "id": "commit-" + n, "name": "Bash", "input": map[string]any{"command": "git commit -am " + quote(message)}}}}})
			add(map[string]any{"type": "user", "uuid": "k" + n, "parentUuid": "c" + n, "timestamp": at(2*time.Minute + 40*time.Second), "promptId": "p" + n, "sourceToolAssistantUUID": "c" + n,
				"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "commit-" + n,
					"content": fmt.Sprintf("[%s %s] %s", pr.branch, head[:7], message)}}}})
			last, answer = "k"+n, "Done and committed as "+head[:7]+". The tests pass."
		}
		add(map[string]any{"type": "assistant", "uuid": "b" + n, "parentUuid": last, "timestamp": at(3 * time.Minute), "requestId": "req-b" + n,
			"message": map[string]any{"id": "msg-b" + n, "role": "assistant", "model": s.model, "stop_reason": "end_turn", "usage": usage,
				"content": []any{map[string]any{"type": "text", "text": answer}}}})
		parent = "b" + n
	}
	path := filepath.Join(home, ".claude", "projects", claudecode.EncodeProjectDir(cwd), s.id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(lines.String()), 0o600)
}

// reply is the agent's answer to a prompt: done, and the tests pass.
func reply(prompt string) string {
	if strings.HasPrefix(prompt, "How ") || strings.HasPrefix(prompt, "The ") {
		return "Here is what I found, and the smallest change that would do it. The tests pass as they are."
	}
	return "Done. The tests pass."
}

// pullNumbered is the sample pull request numbered n.
func pullNumbered(n int) (pull, bool) {
	for _, pl := range append([]pull{newestFirst}, openPulls...) {
		if pl.number == n {
			return pl, true
		}
	}
	return pull{}, false
}

// pullFiles are the files a pull request's commits write.
func pullFiles(pr pull) []string {
	var files []string
	for _, c := range pr.commits {
		for _, file := range changedFiles(c) {
			if !slices.Contains(files, file) {
				files = append(files, file)
			}
		}
	}
	return files
}

// pullText is a file as a pull request's last commit to write it left it.
func pullText(pr pull, file string) string {
	text := ""
	for _, c := range pr.commits {
		if written, ok := c.files[file]; ok {
			text = written
		}
	}
	return text
}
