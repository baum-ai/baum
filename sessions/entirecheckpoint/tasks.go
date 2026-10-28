package entirecheckpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Task is one `tasks/<id>/` directory at the checkpoint root: what Entire kept
// of a subagent the checkpoint's sessions ran. The directory name is the
// spawning tool call (Claude Code) or the child thread (Codex). The checkpoint
// metadata does not list tasks, and nothing in a task names the session that
// spawned it except the transcript's own lines.
//
// A task that cannot be read is kept with Problem set rather than failing the
// checkpoint: one damaged subagent must not cost the sessions their transcripts.
type Task struct {
	Directory string `json:"directory"`
	// Meta is task.json. MetaPresent is false when the directory has none.
	Meta        TaskMeta `json:"meta"`
	MetaPresent bool     `json:"meta_present"`
	// Transcript is the single agent-<id>.jsonl, the subagent's own native
	// transcript, and TranscriptName its file name.
	Transcript        []byte `json:"-"`
	TranscriptName    string `json:"transcript_name,omitempty"`
	TranscriptPresent bool   `json:"transcript_present"`
	Problem           string `json:"problem,omitempty"`
}

// TaskMeta is task.json. Entire writes it for every task, with or without a
// transcript.
type TaskMeta struct {
	ToolUseID       string          `json:"tool_use_id"`
	AgentID         string          `json:"agent_id"`
	SubagentType    string          `json:"subagent_type"`
	TaskDescription string          `json:"task_description"`
	StartedAt       string          `json:"started_at"`
	CompletedAt     string          `json:"completed_at"`
	TokenUsage      json.RawMessage `json:"token_usage,omitempty"`
}

// AgentID is the subagent id the transcript file is named after, falling back
// to task.json.
func (t *Task) AgentID() string {
	if name := strings.TrimSuffix(strings.TrimPrefix(t.TranscriptName, "agent-"), ".jsonl"); name != "" {
		return name
	}
	return t.Meta.AgentID
}

const tasksDir = "tasks"

// parseTasks reads every task directory. A filesystem that cannot list
// directories, or a checkpoint without tasks, yields none.
func parseTasks(fsys fs.FS) ([]*Task, error) {
	entries, err := fs.ReadDir(fsys, tasksDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s: %w", tasksDir, err)
	}
	var tasks []*Task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		task, err := parseTask(fsys, entry.Name())
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Directory < tasks[j].Directory })
	return tasks, nil
}

func parseTask(fsys fs.FS, name string) (*Task, error) {
	dir := path.Join(tasksDir, name)
	task := &Task{Directory: name}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	var transcripts []string
	for _, entry := range entries {
		switch file := entry.Name(); {
		case entry.IsDir():
		case file == "task.json":
			raw, err := fs.ReadFile(fsys, path.Join(dir, file))
			if err != nil {
				return nil, fmt.Errorf("reading %s/task.json: %w", dir, err)
			}
			if err := json.Unmarshal(raw, &task.Meta); err != nil {
				task.Problem = fmt.Sprintf("task.json does not decode: %v", err)
				continue
			}
			task.MetaPresent = true
		case strings.HasPrefix(file, "agent-") && strings.HasSuffix(file, ".jsonl"):
			transcripts = append(transcripts, file)
		}
	}
	switch {
	case len(transcripts) > 1:
		task.Problem = fmt.Sprintf("task has %d agent transcripts, want at most one", len(transcripts))
	case len(transcripts) == 1:
		raw, err := fs.ReadFile(fsys, path.Join(dir, transcripts[0]))
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s: %w", dir, transcripts[0], err)
		}
		task.Transcript, task.TranscriptName, task.TranscriptPresent = raw, transcripts[0], true
	}
	if task.Problem == "" && task.MetaPresent {
		switch {
		case task.Meta.ToolUseID != "" && task.Meta.ToolUseID != name:
			task.Problem = fmt.Sprintf("task.json names tool use %s, its directory %s", task.Meta.ToolUseID, name)
		case task.TranscriptPresent && task.Meta.AgentID != "" && task.Meta.AgentID != task.AgentID():
			task.Problem = fmt.Sprintf("task.json names agent %s, its transcript %s", task.Meta.AgentID, task.TranscriptName)
		}
	}
	return task, nil
}
