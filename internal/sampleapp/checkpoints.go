package sampleapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// Alex works on another computer with Claude Code and Entire, so Alex's
// sessions reach the sample the way a teammate's do: each of Alex's commits
// names an Entire checkpoint, and the checkpoints branch fetched from
// GitHub holds the session that wrote it.
const (
	teammateCwd        = "/home/dev/code/notes"
	checkpointRef      = "refs/remotes/origin/entire/checkpoints/v1"
	entireCLIVersion   = "0.9.4"
	teammateModel      = "claude-opus-4-1"
	checkpointStrategy = "manual-commit"
)

// teammateSession is one of Alex's Claude Code sessions on a pull request:
// what Alex asked, the commit each prompt led to (by its place in the pull
// request), and what it decided.
type teammateSession struct {
	id, title string
	pr        pull
	prompts   []string
	leadsTo   []int
	decided   []decisions.Decision
}

// commits are the commits the session made, in order.
func (s teammateSession) commits() []int {
	var made []int
	for _, n := range s.leadsTo {
		if len(made) == 0 || made[len(made)-1] != n {
			made = append(made, n)
		}
	}
	return made
}

// turnsUntil is how many prompts the session had answered by commit n.
func (s teammateSession) turnsUntil(n int) int {
	turns := 0
	for _, c := range s.leadsTo {
		if c <= n {
			turns++
		}
	}
	return turns
}

// commitsAfter is whether the session committed after answering prompt i.
func (s teammateSession) commitsAfter(i int) bool {
	return i == len(s.leadsTo)-1 || s.leadsTo[i+1] != s.leadsTo[i]
}

var teammateSessions = []teammateSession{
	{id: "a1e70c11-0000-4000-8000-000000000012", title: "Add tags to notes", pr: tags,
		prompts: []string{
			"Notes need tags (NOTES-8). Add a Tags field, and a way to list the notes with one tag.",
			"Clean tags once, when a note is saved: trim them, lowercase them, and drop blanks and repeats.",
			"ByTag shouldn't scan every note. Keep an index from each tag to its notes.",
			"Add a small notes command that lists a JSON file's notes, with -tag to filter.",
		},
		leadsTo: []int{0, 0, 0, 1},
		decided: []decisions.Decision{
			{ID: "dec-tags-clean-on-save", Title: "Clean tags once, when a note is saved",
				Summary:   "Add trims and lowercases tags and drops blank and repeated ones, so every read sees clean tags.",
				Rationale: "Notes are read far more often than they are written, and one place for the rule keeps it consistent.", RejectedAlternative: "Cleaning tags on every read",
				Category: decisions.CategoryArchitecture, Who: decisions.WhoHuman, Turns: []int{2}, Files: []string{"store.go", "tags.go"}},
			{ID: "dec-tags-index", Title: "Keep a tag index instead of scanning",
				Summary:   "The store maps each tag to the positions of its notes, filled by Add, so ByTag reads only the notes it returns.",
				Rationale: "Scanning every note made ByTag slower as the list grew.", RejectedAlternative: "Scanning every note on each call",
				Category: decisions.CategoryImplementation, Who: decisions.WhoHuman, Turns: []int{3}, Files: []string{"store.go", "tags.go"}},
			{ID: "dec-tags-index-positions", Title: "Index positions, with Add as the only writer",
				Summary:   "The index holds positions in the note list, and Add is the only code that changes either, as its comment says.",
				Rationale: "Positions keep the index small; nothing removes or reorders notes yet.",
				Category:  decisions.CategoryImplementation, Who: decisions.WhoAgent, Turns: []int{3}, Files: []string{"store.go"}},
			{ID: "dec-tags-cli-file", Title: "The command reads notes from a JSON file",
				Summary:   "notes -file reads a JSON list of notes and prints them newest first, or only those with -tag.",
				Rationale: "The library keeps notes in memory only, so a file is the simplest thing the command can list.",
				Category:  decisions.CategoryInterface, Who: decisions.WhoApproved, Turns: []int{4}, Files: []string{"cmd/notes/main.go"}},
		}},
	{id: "a1e70c11-0000-4000-8000-000000000013", title: "Fix ByTag after review", pr: tags,
		prompts: []string{
			"Codex says ByTag doesn't trim the tag it's asked for, so ByTag(\" work\") finds nothing. Fix it.",
			"Add tests for cleaning, the order ByTag returns, and the case Codex found, and a benchmark with 10,000 notes.",
			"Mention tags and the command in the README.",
		},
		leadsTo: []int{2, 3, 3},
		decided: []decisions.Decision{
			{ID: "dec-tags-one-rule", Title: "One cleaning rule for saving and asking",
				Summary:   "cleanTag is the single rule: CleanTags uses it when a note is saved and ByTag when a tag is looked up.",
				Rationale: "Two copies of the rule had already drifted apart once.",
				Category:  decisions.CategoryBehavior, Who: decisions.WhoAgent, Turns: []int{1}, Files: []string{"tags.go"}},
			{ID: "dec-tags-benchmark", Title: "Measure ByTag before and after the index",
				Summary:   "BenchmarkByTag lists one tag among 10,000 notes, to show what the index saves.",
				Rationale: "The index is only worth its upkeep if it is measurably faster.",
				Category:  decisions.CategoryScope, Who: decisions.WhoHuman, Turns: []int{2}, Files: []string{"tags_test.go"}},
		}},
}

// turnAt is when prompt i was sent: twelve minutes a prompt, the last
// before a commit ending as it is made.
func (s teammateSession) turnAt(i int, now time.Time) time.Time {
	after := 0
	for j := i + 1; j < len(s.leadsTo) && s.leadsTo[j] == s.leadsTo[i]; j++ {
		after++
	}
	return now.Add(-s.pr.commits[s.leadsTo[i]].ago).Add(-time.Duration(after+1) * 12 * time.Minute)
}

// span is when the session ran: from its first prompt to its last commit.
func (s teammateSession) span(now time.Time) (time.Time, time.Time) {
	made := s.commits()
	return s.turnAt(0, now), now.Add(-s.pr.commits[made[len(made)-1]].ago)
}

// checkpointID is the Entire checkpoint a pull request's commit names.
func checkpointID(pr pull, commit int) string {
	return fmt.Sprintf("a1e70c%04d%02d", pr.number, commit)
}

// checkpointed is whether a pull request's commits name checkpoints.
func checkpointed(pr pull) bool {
	for _, s := range teammateSessions {
		if s.pr.number == pr.number {
			return true
		}
	}
	return false
}

// writeCheckpoints writes the checkpoints Alex's commits name onto the
// fetched checkpoints branch, one commit per checkpoint, as Entire pushes
// them. Each records the session that made the commit, as it was then.
func writeCheckpoints(ctx context.Context, p project, now time.Time) error {
	scratch, err := os.MkdirTemp("", "baum-sample-checkpoints-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	git := func(env []string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = p.dir
		cmd.Env = append(append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME="+alex.name, "GIT_AUTHOR_EMAIL="+alex.login+"@example.com",
			"GIT_COMMITTER_NAME="+alex.name, "GIT_COMMITTER_EMAIL="+alex.login+"@example.com"), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	tree := filepath.Join(scratch, "tree")
	index := filepath.Join(scratch, "index")
	parent := ""
	for _, s := range teammateSessions {
		for _, n := range s.commits() {
			id := checkpointID(s.pr, n)
			at := now.Add(-s.pr.commits[n].ago)
			if err := writeCheckpoint(filepath.Join(tree, id[:2], id[2:]), id, s, p, n, at, now); err != nil {
				return err
			}
			env := []string{"GIT_INDEX_FILE=" + index, "GIT_WORK_TREE=" + tree,
				"GIT_AUTHOR_DATE=" + at.Format(time.RFC3339), "GIT_COMMITTER_DATE=" + at.Format(time.RFC3339)}
			if _, err := git(env, "add", "-A", "."); err != nil {
				return err
			}
			written, err := git(env, "write-tree")
			if err != nil {
				return err
			}
			args := []string{"commit-tree", written, "-m", "Checkpoint: " + id}
			if parent != "" {
				args = append(args, "-p", parent)
			}
			if parent, err = git(env, args...); err != nil {
				return err
			}
		}
	}
	_, err = git(nil, "update-ref", checkpointRef, parent)
	return err
}

// writeCheckpoint writes one checkpoint into dir: its metadata and the
// session that made commit n of the pull request. Each of a session's
// checkpoints carries all of it, so each names every commit it made.
func writeCheckpoint(dir, id string, s teammateSession, p project, n int, at, now time.Time) error {
	turns := s.turnsUntil(n)
	full := teammateTranscript(s, p, now)
	files := changedFiles(s.pr.commits[n])
	refs := func(name string) string { return "/" + id[:2] + "/" + id[2:] + "/0/" + name }
	usage := map[string]any{"input_tokens": 2400 * turns, "cache_creation_tokens": 900, "cache_read_tokens": 15000 * turns, "output_tokens": 520 * turns, "api_call_count": 2 * turns}
	attribution := map[string]any{"calculated_at": at.UTC().Format(time.RFC3339), "agent_lines": 0, "total_committed": 0, "total_lines_changed": 0, "agent_percentage": 92, "metric_version": 2}
	if lines, ok := p.lines[p.commits[s.pr.number][n]]; ok {
		attribution["total_committed"], attribution["total_lines_changed"] = lines[0], lines[0]+lines[1]
		attribution["agent_lines"] = lines[0] * 92 / 100
	}
	root := map[string]any{"cli_version": entireCLIVersion, "checkpoint_id": id, "strategy": checkpointStrategy, "branch": s.pr.branch,
		"checkpoints_count": 1, "files_touched": files,
		"sessions": []any{map[string]any{"metadata": refs("metadata.json"), "transcript": refs("full.jsonl"), "compact_transcript": refs("transcript.jsonl"),
			"content_hash": refs("content_hash.txt"), "prompt": refs("prompt.txt")}},
		"token_usage": usage, "combined_attribution": attribution}
	session := map[string]any{"cli_version": entireCLIVersion, "checkpoint_id": id, "session_id": s.id, "strategy": checkpointStrategy,
		"created_at": at.UTC().Format(time.RFC3339), "branch": s.pr.branch, "checkpoints_count": 1, "save_step_count": 1,
		"files_touched": files, "agent": "Claude Code", "model": teammateModel, "turn_id": fmt.Sprintf("turn-%d", turns-1),
		"compact_transcript_start": 0, "token_usage": usage, "session_metrics": map[string]any{"turn_count": turns}, "initial_attribution": attribution}
	write := func(name string, data []byte) error {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o600)
	}
	asJSON := func(v any) []byte { raw, _ := json.MarshalIndent(v, "", "  "); return append(raw, '\n') }
	for name, data := range map[string][]byte{
		"metadata.json":      asJSON(root),
		"0/metadata.json":    asJSON(session),
		"0/full.jsonl":       full,
		"0/transcript.jsonl": compactTranscript(s, now),
		"0/content_hash.txt": []byte(entirecheckpoint.ContentHashOf(full)),
		"0/prompt.txt":       []byte(strings.Join(s.prompts, "\n---\n")),
	} {
		if err := write(name, data); err != nil {
			return err
		}
	}
	return nil
}

// changedFiles are the files a commit writes, in a stable order.
func changedFiles(c commit) []string {
	var files []string
	for name := range c.files {
		files = append(files, name)
	}
	slices.Sort(files)
	return files
}

// teammateTranscript is a Claude Code transcript of the session, each turn
// writing the files of the commit it leads to and committing them when it
// is the last before that commit.
func teammateTranscript(s teammateSession, p project, now time.Time) []byte {
	var out strings.Builder
	add := func(v map[string]any) {
		v["cwd"], v["gitBranch"], v["version"], v["entrypoint"], v["userType"], v["isSidechain"], v["sessionId"] = teammateCwd, s.pr.branch, "2.1.220", "cli", "external", false, s.id
		raw, _ := json.Marshal(v)
		out.Write(raw)
		out.WriteByte('\n')
	}
	title, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": s.title, "sessionId": s.id})
	out.Write(append(title, '\n'))
	parent := any(nil)
	for i := range s.prompts {
		at := func(d time.Duration) string { return s.turnAt(i, now).Add(d).UTC().Format("2006-01-02T15:04:05.000Z") }
		n := fmt.Sprint(i + 1)
		usage := map[string]any{"input_tokens": 2400, "output_tokens": 520, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 15000}
		add(map[string]any{"type": "user", "uuid": "u" + n, "parentUuid": parent, "timestamp": at(0), "promptId": "p" + n, "promptSource": "typed",
			"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": s.prompts[i]}})
		commitIndex := s.leadsTo[i]
		var uses, results []any
		for j, file := range changedFiles(s.pr.commits[commitIndex]) {
			tool := fmt.Sprintf("edit-%s-%d", n, j)
			uses = append(uses, map[string]any{"type": "tool_use", "id": tool, "name": "Edit",
				"input": map[string]any{"file_path": teammateCwd + "/" + file, "old_string": "", "new_string": s.pr.commits[commitIndex].files[file]}})
			results = append(results, map[string]any{"type": "tool_result", "tool_use_id": tool, "content": "The file " + teammateCwd + "/" + file + " has been updated."})
		}
		tests := "test-" + n
		uses = append(uses, map[string]any{"type": "tool_use", "id": tests, "name": "Bash", "input": map[string]any{"command": "go test ./..."}})
		results = append(results, map[string]any{"type": "tool_result", "tool_use_id": tests, "content": "ok  \texample.com/notes\t0.014s"})
		add(map[string]any{"type": "assistant", "uuid": "a" + n, "parentUuid": "u" + n, "timestamp": at(4 * time.Minute), "requestId": "req-a" + n,
			"message": map[string]any{"id": "msg-a" + n, "role": "assistant", "model": teammateModel, "stop_reason": "tool_use", "usage": usage, "content": uses}})
		add(map[string]any{"type": "user", "uuid": "r" + n, "parentUuid": "a" + n, "timestamp": at(6 * time.Minute), "promptId": "p" + n,
			"sourceToolAssistantUUID": "a" + n, "message": map[string]any{"role": "user", "content": results}})
		last := "r" + n
		text := "Done, and the tests pass."
		if s.commitsAfter(i) {
			sha := p.commits[s.pr.number][commitIndex]
			message := s.pr.commits[commitIndex].message
			commitTool := "commit-" + n
			add(map[string]any{"type": "assistant", "uuid": "c" + n, "parentUuid": last, "timestamp": at(7 * time.Minute), "requestId": "req-c" + n,
				"message": map[string]any{"id": "msg-c" + n, "role": "assistant", "model": teammateModel, "stop_reason": "tool_use", "usage": usage,
					"content": []any{map[string]any{"type": "tool_use", "id": commitTool, "name": "Bash", "input": map[string]any{"command": "git commit -am " + quote(message)}}}}})
			add(map[string]any{"type": "user", "uuid": "k" + n, "parentUuid": "c" + n, "timestamp": at(8 * time.Minute), "promptId": "p" + n,
				"sourceToolAssistantUUID": "c" + n, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": commitTool,
					"content": fmt.Sprintf("[%s %s] %s", s.pr.branch, sha[:7], message)}}}})
			last = "k" + n
			text = "Done and committed as " + sha[:7] + "; the tests pass."
		}
		add(map[string]any{"type": "assistant", "uuid": "b" + n, "parentUuid": last, "timestamp": at(9 * time.Minute), "requestId": "req-b" + n,
			"message": map[string]any{"id": "msg-b" + n, "role": "assistant", "model": teammateModel, "stop_reason": "end_turn", "usage": usage,
				"content": []any{map[string]any{"type": "text", "text": text}}}})
		parent = "b" + n
	}
	return []byte(out.String())
}

// compactTranscript is Entire's compact form of the same turns: each
// prompt and the agent's answer.
func compactTranscript(s teammateSession, now time.Time) []byte {
	var out strings.Builder
	for i := range s.prompts {
		at := s.turnAt(i, now)
		for _, line := range []map[string]any{
			{"v": 1, "agent": "claude-code", "cli_version": entireCLIVersion, "type": "user", "ts": at.UTC().Format(time.RFC3339), "content": []any{map[string]any{"text": s.prompts[i]}}},
			{"v": 1, "agent": "claude-code", "cli_version": entireCLIVersion, "type": "assistant", "ts": at.Add(9 * time.Minute).UTC().Format(time.RFC3339),
				"id": fmt.Sprintf("msg-b%d", i+1), "input_tokens": 2400, "output_tokens": 520, "content": []any{map[string]any{"type": "text", "text": "Done, and the tests pass."}}},
		} {
			raw, _ := json.Marshal(line)
			out.Write(raw)
			out.WriteByte('\n')
		}
	}
	return []byte(out.String())
}

// writeTeammateDecisions records the decisions capture found in Alex's
// sessions, as it does for a linked teammate's session.
func writeTeammateDecisions(ctx context.Context, decided *decisionlog.Store, now time.Time) error {
	for _, s := range teammateSessions {
		times := map[int]time.Time{}
		for i := range s.prompts {
			times[i+1] = s.turnAt(i, now)
		}
		if err := decided.Save(ctx, decisionlog.Extraction{Meta: decisionlog.Meta{Agent: "claude-code", ID: s.id, Repository: repository, Branch: s.pr.branch, Fingerprint: "sample"},
			ContentHash: "sample-" + s.id, Decisions: s.decided, TurnTimes: times}); err != nil {
			return err
		}
	}
	return nil
}

func quote(s string) string { raw, _ := json.Marshal(s); return string(raw) }
