package baumcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
)

// The desktop app answers what Claude Code and Codex ask their user, and
// sends a running session a message, through their hooks: a permission
// request waits on the daemon (agentrequests.MethodRequest) and prints the
// app's decision, a stop takes the message queued for its session
// (agentrequests.MethodTakeMessage). With no daemon, an incompatible one or
// any failure, the hook prints nothing and the agent asks as usual. Neither
// starts a daemon: recording the event keeps its own rules for that.
const (
	permissionRequestEvent = "permission-request"
	postToolUseEvent       = "post-tool-use"
	postToolFailureEvent   = "post-tool-use-failure"
	stopEvent              = "stop"
	// maxFullHookInput bounds the input of the events whose whole payload a
	// hook reads, which it echoes back to the agent; a longer one is left to
	// the agent's own prompt.
	maxFullHookInput = 16 << 20
	// fullInputWait is how long such a hook waits for its input to end.
	fullInputWait = 2 * time.Second
	// codexRequestWait bounds a Codex request beyond the daemon's own wait
	// (agentrequests.DefaultCodexWait), inside the hook's 30-second timeout.
	codexRequestWait = 28 * time.Second
	// takeMessageBudget bounds a stop's question to the daemon: a Stop hook
	// is never held.
	takeMessageBudget = 300 * time.Millisecond
	// maxAskBytes keeps an encoded request well inside the control
	// protocol's 64 KiB line.
	maxAskBytes = 56 << 10
	// maxAskValue and maxAskLabel bound what a hook sends before the
	// daemon masks it and cuts it to what the app shows, so the mask sees
	// past the cut.
	maxAskValue = agentrequests.MaxValue + 2000
	maxAskLabel = 600
	// maxShownEdits is how many of a MultiEdit's edits get rows of their
	// own; the rest are shown as JSON.
	maxShownEdits = 8
)

// Claude Code's environment says whether anyone can answer in the terminal:
// an interactive session has the entrypoint "cli" and is attended ("1");
// `claude -p` and the SDK are not. Without either variable the hook holds.
const (
	claudeEntrypointEnv = "CLAUDE_CODE_ENTRYPOINT"
	claudeAttendedEnv   = "CLAUDE_CODE_SESSION_ATTENDED"
)

// What the agent is told of a request the user turned down in the app.
const (
	deniedMessage   = "The user denied this request in the baum app."
	declinedMessage = "The user declined to answer in the baum app."
)

// appAgent reports whether the app answers the agent's requests.
func appAgent(agent string) bool {
	return agent == agentrequests.AgentClaudeCode || agent == agentrequests.AgentCodex
}

// readsFullInput reports whether a hook reads its whole payload: a
// permission request, whose input the answer may echo, and a finished tool
// call, whose input it digests.
func readsFullInput(agent, event string) bool {
	return appAgent(agent) && (event == permissionRequestEvent || digestsToolCall(agent, event))
}

// digestsToolCall reports whether a hook's event carries the digest of its
// tool call: one that finished, or failed.
func digestsToolCall(agent, event string) bool {
	return appAgent(agent) && (event == postToolUseEvent || event == postToolFailureEvent)
}

// unattendedClaude reports a Claude Code session nobody answers in a
// terminal, which a held hook would only stall.
func unattendedClaude() bool {
	entrypoint, attended := os.Getenv(claudeEntrypointEnv), os.Getenv(claudeAttendedEnv)
	return (entrypoint != "" && entrypoint != "cli") || attended == "0"
}

// toolDigest is the digest of a finished tool call's name and input, empty
// when the payload is incomplete or carries no tool.
func toolDigest(input *hookInput) string {
	payload, ok := input.complete(fullInputWait)
	if !ok {
		return ""
	}
	var call struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(payload, &call) != nil || call.ToolName == "" || len(call.ToolInput) == 0 {
		return ""
	}
	return agentrequests.Digest(call.ToolName, call.ToolInput)
}

// answerFromApp shows a permission request in the app and prints its
// decision for the agent, or nothing.
func answerFromApp(ctx context.Context, agent string, input *hookInput, started time.Time, stdout io.Writer) {
	if agent == agentrequests.AgentClaudeCode && unattendedClaude() {
		return
	}
	payload, ok := input.complete(fullInputWait)
	if !ok {
		return
	}
	prompt, ok := parsePermissionRequest(agent, payload)
	if !ok {
		return
	}
	paths, err := hookDaemonPaths()
	if err != nil {
		return
	}
	if agent == agentrequests.AgentCodex {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, codexRequestWait)
		defer cancel()
	}
	ask := prompt.ask
	ask.StartedAt = started.UTC()
	client := &daemon.Client{Paths: paths, Version: version()}
	response, err := client.Wait(ctx, agentrequests.MethodRequest, ask)
	if err != nil || !response.OK {
		return
	}
	var decision agentrequests.Decision
	if json.Unmarshal(response.Result, &decision) != nil {
		return
	}
	if out, ok := prompt.output(decision); ok {
		_, _ = stdout.Write(append(out, '\n'))
	}
}

// finishStop writes a stop hook's output: Entire's, with the message the
// user queued in the app for the session when there is one and the output
// can carry it and the daemon took the stop's event. Entire's output that
// already decides, or is not a JSON object, is written as it is, and the
// message stays queued.
func finishStop(ctx context.Context, agent string, session *hookevents.Session, entire []byte, entireErr error, delivered bool, stdout io.Writer) {
	out := entire
	if _, ok := stopOutput(entire, ""); ok && delivered && entireErr == nil && session != nil && session.ID != "" {
		if text := takeMessage(ctx, agent, session.ID); text != "" {
			if merged, ok := stopOutput(entire, text); ok {
				out = append(merged, '\n')
			}
		}
	}
	_, _ = stdout.Write(out)
}

// holdOutput is a file Entire writes a stop's output to, nil when none can
// be made (Entire then writes to the hook's output, and no message is
// delivered). A file rather than a pipe: a process Entire leaves running
// with its output open would hold a pipe, and the hook with it, open.
func holdOutput() *os.File {
	f, err := os.CreateTemp("", "baum-stop-*")
	if err != nil {
		return nil
	}
	return f
}

// heldOutput is what was written to a holdOutput file, which it removes.
func heldOutput(f *os.File) []byte {
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	data, _ := io.ReadAll(f)
	return data
}

func takeMessage(ctx context.Context, agent, session string) string {
	paths, err := hookDaemonPaths()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, takeMessageBudget)
	defer cancel()
	client := &daemon.Client{Paths: paths, Version: version()}
	response, err := client.Call(ctx, agentrequests.MethodTakeMessage, agentrequests.TakeParams{Agent: agent, SessionID: session})
	if err != nil || !response.OK {
		return ""
	}
	var taken agentrequests.Taken
	if json.Unmarshal(response.Result, &taken) != nil {
		return ""
	}
	return taken.Text
}

// stopOutput is a stop hook's output that makes the agent go on with the
// user's message, which both agents read from "reason": the output Entire
// wrote, when it is empty or a JSON object that does not decide already,
// with the decision added. With no text it only reports whether the output
// can carry one.
func stopOutput(entire []byte, text string) ([]byte, bool) {
	members := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(entire); len(trimmed) > 0 {
		if json.Unmarshal(trimmed, &members) != nil || members == nil {
			return nil, false
		}
		if _, decided := members["decision"]; decided {
			return nil, false
		}
		if keepGoing, ok := members["continue"]; ok && string(keepGoing) == "false" {
			return nil, false
		}
	}
	if text == "" {
		return nil, true
	}
	members["decision"], _ = json.Marshal("block")
	members["reason"], _ = json.Marshal(messageReason(text))
	out, err := encodeJSON(members)
	return out, err == nil
}

// messageReason tells the agent where the message comes from.
func messageReason(text string) string {
	return "The user sent you this message from the baum app while you were working:\n\n" + text
}

// permissionPrompt is a permission request as the agent sent it, and what
// the app is shown of it.
type permissionPrompt struct {
	agent string
	tool  string
	// input is the tool's input as sent, nil when it is not a JSON object.
	input       map[string]json.RawMessage
	suggestions []json.RawMessage
	questions   []claudeQuestion
	ask         agentrequests.Ask
}

type claudeQuestion struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

// parsePermissionRequest reads a PermissionRequest payload.
func parsePermissionRequest(agent string, payload []byte) (*permissionPrompt, bool) {
	var request struct {
		SessionID   string            `json:"session_id"`
		AgentID     string            `json:"agent_id"`
		ToolName    string            `json:"tool_name"`
		ToolInput   json.RawMessage   `json:"tool_input"`
		Suggestions []json.RawMessage `json:"permission_suggestions"`
	}
	if json.Unmarshal(payload, &request) != nil || request.SessionID == "" || request.ToolName == "" || !appAgent(agent) {
		return nil, false
	}
	p := &permissionPrompt{agent: agent, tool: request.ToolName}
	_ = json.Unmarshal(request.ToolInput, &p.input)
	p.ask = agentrequests.Ask{
		Agent: agent, SessionID: request.SessionID, AgentID: request.AgentID, Kind: agentrequests.KindPermission, Tool: request.ToolName,
		Digest: agentrequests.Digest(request.ToolName, request.ToolInput),
	}
	if agent == agentrequests.AgentClaudeCode && request.ToolName == "AskUserQuestion" {
		if json.Unmarshal(p.input["questions"], &p.questions) != nil || len(p.questions) == 0 || len(p.questions) > agentrequests.MaxQuestions {
			return nil, false
		}
		p.ask.Kind = agentrequests.KindQuestion
		for _, q := range p.questions {
			if len(q.Options) > agentrequests.MaxOptions {
				return nil, false
			}
			shown := agentrequests.Question{Question: cutText(q.Question, maxAskLabel), Header: cutText(q.Header, maxAskLabel), MultiSelect: q.MultiSelect}
			for _, o := range q.Options {
				shown.Options = append(shown.Options, agentrequests.Option{Label: cutText(o.Label, maxAskLabel), Description: cutText(o.Description, maxAskLabel)})
			}
			p.ask.Questions = append(p.ask.Questions, shown)
		}
	} else {
		p.ask.Input = inputRows(agent, request.ToolName, p.input, request.ToolInput)
	}
	if agent == agentrequests.AgentClaudeCode {
		p.suggestions = request.Suggestions[:min(len(request.Suggestions), agentrequests.MaxSuggestions)]
		for _, suggestion := range p.suggestions {
			p.ask.Suggestions = append(p.ask.Suggestions, cutText(suggestionLabel(suggestion), maxAskLabel))
		}
	}
	boundAsk(&p.ask)
	return p, true
}

// output is the agent's answer to the app's decision, false when it
// decides nothing.
func (p *permissionPrompt) output(d agentrequests.Decision) ([]byte, bool) {
	decision := map[string]any{"behavior": d.Behavior}
	switch d.Behavior {
	case agentrequests.BehaviorDeny:
		decision["message"] = deniedMessage
		if p.ask.Kind == agentrequests.KindQuestion {
			decision["message"] = declinedMessage
		}
	case agentrequests.BehaviorAllow:
		if p.agent == agentrequests.AgentCodex {
			// Codex fails closed on anything but the behaviour.
			break
		}
		if p.ask.Kind == agentrequests.KindQuestion {
			answers, ok := p.answers(d.Answers)
			if !ok {
				return nil, false
			}
			updated := make(map[string]json.RawMessage, len(p.input)+1)
			for name, value := range p.input {
				updated[name] = value
			}
			updated["answers"], _ = json.Marshal(answers)
			decision["updatedInput"] = updated
		} else if p.tool == "ExitPlanMode" && p.input != nil {
			// Claude Code needs a plan's approval to carry its input.
			decision["updatedInput"] = p.input
		}
		if d.Suggestion != nil {
			if *d.Suggestion < 0 || *d.Suggestion >= len(p.suggestions) {
				return nil, false
			}
			// One suggestion only: echoing them all applies every one,
			// a mode change among them.
			decision["updatedPermissions"] = []json.RawMessage{p.suggestions[*d.Suggestion]}
		}
	default:
		return nil, false
	}
	out, err := encodeJSON(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision}})
	return out, err == nil
}

// answers maps each question's own text to the labels chosen, joined by
// ", ", or to the user's text.
func (p *permissionPrompt) answers(given []agentrequests.Answer) (map[string]string, bool) {
	if len(given) != len(p.questions) {
		return nil, false
	}
	answers := make(map[string]string, len(given))
	for i, answer := range given {
		q := p.questions[i]
		labels := make([]string, 0, len(answer.Options))
		for _, option := range answer.Options {
			if option < 0 || option >= len(q.Options) {
				return nil, false
			}
			labels = append(labels, q.Options[option].Label)
		}
		value := strings.Join(labels, ", ")
		if len(labels) == 0 {
			value = answer.Text
		}
		if value == "" {
			return nil, false
		}
		answers[q.Question] = value
	}
	return answers, true
}

// inputRows describe what a tool call would do, as the app shows it: rows
// for what the tool's input means (a Bash command, an edit's file and text),
// rows for the switches that change what approving it allows (a command run
// outside the sandbox or in the background, an edit of every occurrence, a
// subagent's type and model), then every other member that is not empty as
// one compact JSON row, so nothing the approval covers goes unshown.
func inputRows(agent, tool string, input map[string]json.RawMessage, raw json.RawMessage) []agentrequests.Row {
	var rows []agentrequests.Row
	shown := map[string]bool{}
	row := func(label, value string, code bool) {
		rows = append(rows, agentrequests.Row{Label: label, Value: value, Code: code})
	}
	add := func(label, member string, code bool) {
		if value := memberText(input, member); value != "" {
			row(label, value, code)
			shown[member] = true
		}
	}
	// A switch that is off changes nothing, so it is shown only when on.
	flag := func(label, value, member string) {
		var on bool
		if json.Unmarshal(input[member], &on) == nil {
			shown[member] = true
			if on {
				row(label, value, false)
			}
		}
	}
	scalar := func(label, member string) {
		if value := memberScalar(input, member); value != "" {
			row(label, value, true)
			shown[member] = true
		}
	}
	switch {
	case tool == "Bash":
		add("Command", "command", true)
		add("Description", "description", false)
		flag("Sandbox", "Off: runs outside the sandbox", "dangerouslyDisableSandbox")
		flag("Background", "Runs in the background", "run_in_background")
	case agent == agentrequests.AgentCodex && tool == "apply_patch":
		add("Patch", "command", true)
	case agent == agentrequests.AgentCodex:
	case tool == "Edit":
		add("File", "file_path", true)
		add("Replace", "old_string", true)
		add("With", "new_string", true)
		flag("Replace all", "Every occurrence", "replace_all")
	case tool == "MultiEdit":
		add("File", "file_path", true)
		var edits []map[string]json.RawMessage
		if json.Unmarshal(input["edits"], &edits) == nil && len(edits) > 0 {
			shown["edits"] = true
			row("Edits", strconv.Itoa(len(edits)), false)
			for i, edit := range edits[:min(len(edits), maxShownEdits)] {
				prefix := "Edit " + strconv.Itoa(i+1) + ": "
				var all bool
				_ = json.Unmarshal(edit["replace_all"], &all)
				replace := prefix + "replace"
				if all {
					replace = prefix + "replace every occurrence of"
				}
				row(replace, memberText(edit, "old_string"), true)
				row(prefix+"with", memberText(edit, "new_string"), true)
			}
			if len(edits) > maxShownEdits {
				rest, _ := json.Marshal(edits[maxShownEdits:])
				row("More edits", string(rest), true)
			}
		}
	case tool == "Write":
		add("File", "file_path", true)
		add("Content", "content", true)
	case tool == "NotebookEdit":
		add("Notebook", "notebook_path", true)
		scalar("Edit mode", "edit_mode")
		scalar("Cell", "cell_id")
		add("Source", "new_source", true)
	case tool == "Read":
		add("File", "file_path", true)
	case tool == "Glob" || tool == "Grep":
		add("Pattern", "pattern", true)
		add("Path", "path", true)
	case tool == "WebFetch":
		add("URL", "url", true)
		add("Prompt", "prompt", false)
	case tool == "WebSearch":
		add("Query", "query", false)
	case tool == "ExitPlanMode":
		add("Plan", "plan", false)
	case tool == "Agent" || tool == "Task":
		add("Description", "description", false)
		scalar("Subagent", "subagent_type")
		scalar("Model", "model")
		add("Prompt", "prompt", false)
	}
	label := "Other input"
	if len(rows) == 0 {
		label = "Input"
	}
	if input == nil {
		var compact bytes.Buffer
		if json.Compact(&compact, raw) == nil && !emptyJSON(compact.Bytes()) {
			row(label, compact.String(), true)
		}
	} else {
		other := map[string]json.RawMessage{}
		for name, value := range input {
			var compact bytes.Buffer
			if !shown[name] && json.Compact(&compact, value) == nil && !emptyJSON(compact.Bytes()) {
				other[name] = compact.Bytes()
			}
		}
		if len(other) > 0 {
			encoded, err := encodeJSON(other)
			if err == nil {
				row(label, string(encoded), true)
			}
		}
	}
	for i := range rows {
		rows[i].Value = cutText(rows[i].Value, maxAskValue)
	}
	return rows
}

// emptyJSON reports a value that says nothing: null, "", [] or {}.
func emptyJSON(value []byte) bool {
	switch string(value) {
	case "", "null", `""`, "[]", "{}":
		return true
	}
	return false
}

// memberScalar is a member that is a string, number or boolean, as text.
func memberScalar(input map[string]json.RawMessage, member string) string {
	var text string
	if json.Unmarshal(input[member], &text) == nil {
		return text
	}
	var value any
	if json.Unmarshal(input[member], &value) == nil {
		switch value.(type) {
		case float64, bool:
			return string(bytes.TrimSpace(input[member]))
		}
	}
	return ""
}

// memberText is a member that is a string, or an array of strings joined by
// spaces (a command given as its arguments).
func memberText(input map[string]json.RawMessage, member string) string {
	var text string
	if json.Unmarshal(input[member], &text) == nil {
		return text
	}
	var words []string
	if json.Unmarshal(input[member], &words) == nil {
		return strings.Join(words, " ")
	}
	return ""
}

// suggestionLabel names a Claude Code permission suggestion for the user:
// "Always allow Bash(make:*) in this project".
func suggestionLabel(raw json.RawMessage) string {
	var entry struct {
		Type        string   `json:"type"`
		Behavior    string   `json:"behavior"`
		Destination string   `json:"destination"`
		Mode        string   `json:"mode"`
		Directories []string `json:"directories"`
		Rules       []struct {
			ToolName    string `json:"toolName"`
			RuleContent string `json:"ruleContent"`
		} `json:"rules"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		return "Apply the suggested permission change"
	}
	rules := make([]string, 0, len(entry.Rules))
	for _, rule := range entry.Rules {
		if rule.RuleContent == "" {
			rules = append(rules, rule.ToolName)
		} else {
			rules = append(rules, rule.ToolName+"("+rule.RuleContent+")")
		}
	}
	where := map[string]string{
		"session":         " for this session",
		"localSettings":   " in this project",
		"projectSettings": " in this project, for everyone",
		"userSettings":    " everywhere",
	}[entry.Destination]
	switch entry.Type {
	case "addRules":
		verb := map[string]string{"allow": "Always allow", "deny": "Always deny", "ask": "Always ask before"}[entry.Behavior]
		if verb == "" {
			verb = "Add the rule"
		}
		return verb + " " + strings.Join(rules, ", ") + where
	case "replaceRules":
		return "Replace the " + entry.Behavior + " rules with " + strings.Join(rules, ", ") + where
	case "removeRules":
		return "Remove the " + entry.Behavior + " rules " + strings.Join(rules, ", ") + where
	case "setMode":
		return "Switch to " + entry.Mode + " mode" + where
	case "addDirectories":
		return "Allow access to " + strings.Join(entry.Directories, ", ") + where
	case "removeDirectories":
		return "Remove access to " + strings.Join(entry.Directories, ", ") + where
	}
	return "Apply the suggested permission change"
}

// boundAsk halves the longest string of a request until it encodes within
// maxAskBytes; escaping can make a string several times its length.
func boundAsk(ask *agentrequests.Ask) {
	for range 64 {
		data, err := json.Marshal(ask)
		if err != nil || len(data) <= maxAskBytes {
			return
		}
		var longest *string
		consider := func(s *string) {
			if longest == nil || len(*s) > len(*longest) {
				longest = s
			}
		}
		for i := range ask.Input {
			consider(&ask.Input[i].Value)
		}
		for i := range ask.Suggestions {
			consider(&ask.Suggestions[i])
		}
		for i := range ask.Questions {
			q := &ask.Questions[i]
			consider(&q.Question)
			for j := range q.Options {
				consider(&q.Options[j].Label)
				consider(&q.Options[j].Description)
			}
		}
		if longest == nil || *longest == "" {
			return
		}
		*longest = cutText(*longest, utf8.RuneCountInString(*longest)/2)
	}
}

// cutText cuts text to limit characters, marking the cut.
func cutText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	if limit <= 1 {
		return "…"
	}
	return string([]rune(text)[:limit-1]) + "…"
}

// encodeJSON encodes v without escaping <, > and &, as agents print them.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
