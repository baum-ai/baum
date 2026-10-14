package codexsession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baumai/baum/sessions/codextranscript"
	"github.com/baumai/baum/sessions/session"
)

type toolEvidence struct {
	state       *threadState
	use         *session.ToolUse
	callID      string
	callLine    codextranscript.Line
	command     string
	output      json.RawMessage
	outputSeen  bool
	outputLine  codextranscript.Line
	completions []codextranscript.Line
	interrupted bool
	error       bool
	ok          bool
	errorText   string
}

func callKey(threadID, callID string) string { return threadID + "\x00" + callID }

func (f *threadState) addFunctionCall(line codextranscript.Line, call *codextranscript.FunctionCall) error {
	input := json.RawMessage(call.Arguments)
	if !json.Valid(input) {
		input, _ = json.Marshal(call.Arguments)
	}
	metadata := map[string]string{}
	if call.Namespace != "" {
		metadata["namespace"] = call.Namespace
	}
	if err := f.addTool(line, call.CallID, call.ID, call.Name, input, metadata, ""); err != nil {
		return err
	}
	if evidence := f.b.toolByCall[callKey(f.meta.ID, call.CallID)]; evidence != nil {
		evidence.command = commandFromInput(input)
	}
	return nil
}

func (f *threadState) addCustomCall(line codextranscript.Line, call *codextranscript.CustomToolCall) error {
	input, _ := json.Marshal(call.Input)
	if err := f.addTool(line, call.CallID, call.ID, call.Name, input, nil, call.Status); err != nil {
		return err
	}
	if evidence := f.b.toolByCall[callKey(f.meta.ID, call.CallID)]; evidence != nil {
		evidence.command = call.Input
	}
	return nil
}

func (f *threadState) addToolSearchCall(line codextranscript.Line, call *codextranscript.ToolSearchCall) error {
	callID := call.CallID
	if callID == "" {
		callID = fmt.Sprintf("line-%d", line.Ordinal)
	}
	return f.addTool(line, callID, call.ID, "tool_search", call.Arguments, nil, call.Status)
}

func (f *threadState) addWebSearchCall(line codextranscript.Line, call *codextranscript.WebSearchCall) error {
	locator := fmt.Sprintf("%s:%d", f.meta.ID, line.Ordinal)
	input := call.Action
	if len(input) == 0 || bytes.Equal(input, []byte("null")) {
		input = json.RawMessage(`{}`)
	}
	if err := f.addTool(line, locator, "", "web_search", input, nil, call.Status); err != nil {
		return err
	}
	evidence := f.b.toolByCall[callKey(f.meta.ID, locator)]
	if terminalOK(call.Status) || terminalError(call.Status) || terminalInterrupted(call.Status) {
		evidence.completions = append(evidence.completions, line)
		applyStatus(evidence, call.Status)
	}
	return nil
}

func (f *threadState) addTool(line codextranscript.Line, callID, itemID, name string, input json.RawMessage, metadata any, sourceStatus string) error {
	if callID == "" {
		return fmt.Errorf("%s tool call has no call_id", name)
	}
	key := callKey(f.meta.ID, callID)
	if existing := f.b.toolByCall[key]; existing != nil {
		return fmt.Errorf("tool call id %s repeats line %d", callID, existing.callLine.Ordinal)
	}
	invocation := f.startBatch(line)
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	metadataRaw := json.RawMessage(`{}`)
	if metadata != nil {
		metadataRaw, _ = json.Marshal(metadata)
	}
	row := &session.ToolUse{
		ID: f.ids.Mint("tool_use", callID), SessionID: f.session.ID,
		UserInputID: f.activeInput.ID, ModelInvocationID: invocation.ID,
		Name: name, Verb: toolVerb(name), Input: input, Status: session.StatusNoResult,
		Metadata: metadataRaw, Timestamp: timePtr(line.Timestamp),
	}
	evidence := &toolEvidence{state: f, use: row, callID: callID, callLine: line}
	applyStatus(evidence, sourceStatus)
	f.tools = append(f.tools, row)
	f.b.toolByCall[key], f.b.toolByID[row.ID] = evidence, evidence
	kind, locator := "call_id", callID
	if strings.HasPrefix(callID, f.meta.ID+":") || strings.HasPrefix(callID, "line-") {
		kind, locator = "rollout.line", fmt.Sprintf("%s:%d", f.meta.ID, line.Ordinal)
	}
	if itemID != "" {
		f.vendorRow(row.ID, session.EntityToolUse, "item.id", itemID, line.Ordinal)
	}
	f.vendorRow(row.ID, session.EntityToolUse, kind, locator, line.Ordinal)
	f.addTouchesFromTool(evidence)
	return nil
}

func toolVerb(name string) session.ToolVerb {
	name = strings.ToLower(name)
	switch {
	case name == "apply_patch" || strings.Contains(name, "edit_file") || strings.Contains(name, "write_file") || strings.Contains(name, "notebook_edit"):
		return session.VerbEdit
	case strings.Contains(name, "read_file") || strings.Contains(name, "read_text") || strings.Contains(name, "view_image") || strings.Contains(name, "notebook_read"):
		return session.VerbRead
	case strings.Contains(name, "search") || strings.Contains(name, "grep") || strings.Contains(name, "glob") || strings.Contains(name, "fetch"):
		return session.VerbSearch
	case name == "exec_command" || name == "shell" || name == "shell_command" || name == "bash" || name == "exec" || name == "write_stdin" || name == "wait":
		return session.VerbExecute
	default:
		return session.VerbOther
	}
}

func (f *threadState) resolveSpawn(child *codextranscript.File) (*session.SpawnLink, error) {
	var matches []*toolEvidence
	for _, activity := range f.activities {
		if activity.activity.Kind != "started" || activity.activity.AgentThreadID != child.Meta.ID || activity.activity.AgentPath != child.Meta.AgentPath {
			continue
		}
		if evidence := f.b.toolByCall[callKey(f.meta.ID, activity.activity.EventID)]; evidence != nil {
			matches = append(matches, evidence)
		}
	}
	if len(matches) == 0 {
		for _, evidence := range f.b.toolByID {
			if evidence.state != f || explicitAgentID(evidence.output) != child.Meta.ID {
				continue
			}
			matches = append(matches, evidence)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("child rollout %s resolves to %d parent spawn tool uses, want exactly one", child.Path, len(matches))
	}
	evidence := matches[0]
	evidence.use.Verb = session.VerbSpawnSubagent
	evidence.ok = true
	return &session.SpawnLink{ParentSessionID: f.session.ID, ToolUseID: evidence.use.ID}, nil
}

func explicitAgentID(raw json.RawMessage) string {
	var object struct {
		AgentID string `json:"agent_id"`
	}
	if json.Unmarshal(raw, &object) == nil && object.AgentID != "" {
		return object.AgentID
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil && json.Unmarshal([]byte(encoded), &object) == nil {
		return object.AgentID
	}
	return ""
}
