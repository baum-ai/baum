package livesessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// Bounds on the activity kept of a session: the steps served, the longest
// target and message served, and how many distinct files are remembered, so
// a session that writes without end cannot grow the set without end.
const (
	maxSteps       = 12
	maxTarget      = 160
	maxLastMessage = 1200
	maxFilesKept   = 10000
	// maxRawTarget bounds what is kept of a tool's input before it is
	// masked and cut for serving.
	maxRawTarget = 4 << 10
)

// Activity is what a session's transcript says it is doing, as the list
// serves it.
type Activity struct {
	// Steps are the latest tool calls, oldest first, at most maxSteps.
	Steps []Step `json:"steps,omitempty"`
	// FilesTouched is the number of distinct files the session wrote, over
	// the whole transcript read so far; absent when none.
	FilesTouched int `json:"files_touched,omitempty"`
	// LastMessage is the latest assistant text, masked, its line breaks kept
	// (block) and cut.
	LastMessage string `json:"last_message,omitempty"`
}

// Step is one tool call: the tool as the transcript names it, normalised for
// display, and what it was aimed at, masked, on one line and cut, with the
// time of the transcript entry when it has one (RFC 3339, UTC).
type Step struct {
	Tool   string `json:"tool"`
	Target string `json:"target,omitempty"`
	At     string `json:"at,omitempty"`
}

// rawStep is a step as read: its target neither masked nor cut, and whether
// it is a file path, to be made relative to the session's working directory
// when it is served.
type rawStep struct {
	tool, target, at string
	path             bool
}

// activityScan is what has been read of a transcript's activity.
type activityScan struct {
	steps []rawStep
	// files are the paths written, as given and cleaned, at most
	// maxFilesKept of them.
	files   map[string]struct{}
	message string
}

func (a *activityScan) addStep(s rawStep) {
	if s.tool == "" {
		return
	}
	if len(s.target) > maxRawTarget {
		s.target = s.target[:maxRawTarget]
	}
	a.steps = append(a.steps, s)
	if len(a.steps) > maxSteps {
		a.steps = append(a.steps[:0], a.steps[len(a.steps)-maxSteps:]...)
	}
}

func (a *activityScan) addFile(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if a.files == nil {
		a.files = map[string]struct{}{}
	}
	if len(a.files) < maxFilesKept {
		a.files[filepath.Clean(path)] = struct{}{}
	}
}

// activity is the scan as served for a session working in cwd, nil when
// nothing was found.
func (t *Transcripts) activity(a *activityScan, cwd string) *Activity {
	out := &Activity{FilesTouched: len(a.files), LastMessage: t.Tracker.block(a.message, maxLastMessage)}
	for _, s := range a.steps {
		target := s.target
		if s.path {
			target = relativeTo(cwd, target)
		}
		step := Step{Tool: s.tool, Target: t.Tracker.oneLine(target, maxTarget)}
		if at, err := time.Parse(time.RFC3339Nano, s.at); err == nil {
			step.At = at.UTC().Format(time.RFC3339)
		}
		out.Steps = append(out.Steps, step)
	}
	if len(out.Steps) == 0 && out.FilesTouched == 0 && out.LastMessage == "" {
		return nil
	}
	return out
}

// relativeTo is path relative to dir when it lies inside it, else as given.
func relativeTo(dir, path string) string {
	if dir == "" || !filepath.IsAbs(path) || !filepath.IsAbs(dir) {
		return path
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

// displayTool is a tool's name as served: an MCP tool, mcp__server__tool,
// as "server: tool"; Codex's shell tools as Bash and apply_patch as Edit.
func displayTool(name string) string {
	switch name {
	case "shell", "exec_command", "local_shell", "shell_command":
		return "Bash"
	case "apply_patch":
		return "Edit"
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if server, tool, ok := strings.Cut(rest, "__"); ok {
			return server + ": " + tool
		}
	}
	return name
}

// firstLine is the first non-blank line of text.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// mayHaveActivity is a cheap test of a Claude Code line before it is decoded.
func mayHaveActivity(line []byte) bool {
	return bytes.Contains(line, []byte(`"type":"assistant"`))
}

// claudeActivity reads an assistant line of a Claude Code transcript: its
// tool calls as steps and the files they write, and its text as the latest
// message.
func (a *activityScan) claudeActivity(line []byte) {
	var v struct {
		Timestamp string `json:"timestamp"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &v) != nil {
		return
	}
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(v.Message.Content, &blocks) != nil {
		return
	}
	var texts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				texts = append(texts, b.Text)
			}
		case "tool_use":
			var in struct {
				FilePath     string `json:"file_path"`
				NotebookPath string `json:"notebook_path"`
				Path         string `json:"path"`
				Command      string `json:"command"`
				Pattern      string `json:"pattern"`
				URL          string `json:"url"`
				Query        string `json:"query"`
				Description  string `json:"description"`
				Skill        string `json:"skill"`
			}
			_ = json.Unmarshal(b.Input, &in)
			step := rawStep{tool: displayTool(b.Name), at: v.Timestamp}
			switch b.Name {
			case "Read":
				step.target, step.path = in.FilePath, true
			case "Edit", "Write", "MultiEdit":
				step.target, step.path = in.FilePath, true
				a.addFile(in.FilePath)
			case "NotebookEdit":
				step.target, step.path = in.NotebookPath, true
				a.addFile(in.NotebookPath)
			case "Bash":
				step.target = firstLine(in.Command)
			case "Grep":
				step.target = `"` + in.Pattern + `"`
				if in.Path != "" {
					step.target += " in " + in.Path
				}
			case "Glob":
				step.target = in.Pattern
			case "WebFetch":
				step.target = in.URL
			case "WebSearch":
				step.target = in.Query
			case "Agent", "Task":
				step.target = in.Description
			case "Skill":
				step.target = in.Skill
			}
			a.addStep(step)
		}
	}
	if len(texts) > 0 {
		a.message = strings.Join(texts, "\n")
	}
}

// codexActivity reads a response_item line of a Codex rollout: a function or
// custom tool call as a step (and, for apply_patch, the files its headers
// name), an assistant message as the latest message.
func (a *activityScan) codexActivity(line []byte) {
	var v struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Role      string          `json:"role"`
			Arguments string          `json:"arguments"`
			Input     string          `json:"input"`
			Action    json.RawMessage `json:"action"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &v) != nil || v.Type != "response_item" {
		return
	}
	p := v.Payload
	switch p.Type {
	case "message":
		if p.Role != "assistant" {
			return
		}
		var texts []string
		for _, c := range p.Content {
			if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
				texts = append(texts, c.Text)
			}
		}
		if len(texts) > 0 {
			a.message = strings.Join(texts, "\n")
		}
	case "function_call", "custom_tool_call", "local_shell_call":
		name := p.Name
		if p.Type == "local_shell_call" {
			name = "local_shell"
		}
		step := rawStep{tool: displayTool(name), at: v.Timestamp}
		var args struct {
			Cmd     string          `json:"cmd"`
			Command json.RawMessage `json:"command"`
			Input   string          `json:"input"`
		}
		_ = json.Unmarshal([]byte(p.Arguments), &args)
		if len(p.Action) > 0 {
			var action struct {
				Command json.RawMessage `json:"command"`
			}
			if json.Unmarshal(p.Action, &action) == nil {
				args.Command = action.Command
			}
		}
		switch step.tool {
		case "Bash":
			step.target = firstLine(cmp.Or(args.Cmd, shellCommand(args.Command)))
		case "Edit":
			files := patchFiles(cmp.Or(p.Input, args.Input))
			for _, f := range files {
				a.addFile(f)
			}
			if len(files) > 0 {
				step.target, step.path = files[0], true
			}
		}
		a.addStep(step)
	}
}

// shellCommand is the command of a Codex shell call: a string, or an argv
// array, of which a "bash -lc <script>" form is its script.
func shellCommand(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return ""
	}
	if len(argv) >= 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return argv[2]
	}
	return strings.Join(argv, " ")
}

// patchFiles are the files an apply_patch body writes, from its file
// headers, in order.
func patchFiles(patch string) []string {
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Move to: "} {
			if path, ok := strings.CutPrefix(line, prefix); ok && strings.TrimSpace(path) != "" {
				out = append(out, strings.TrimSpace(path))
			}
		}
	}
	return out
}
