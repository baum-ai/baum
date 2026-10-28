package codexsession

import (
	"encoding/json"
	"fmt"
	"github.com/baum-ai/baum/sessions/traceprocessing"
	"sort"
	"strings"

	"github.com/baum-ai/baum/linkage/gitevidence"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/session"
)

func (f *threadState) toolOutput(line codextranscript.Line, callID string, output json.RawMessage) (accountCategory, string, error) {
	if f.unsupportedCall[callID] {
		f.batch = nil
		return accountDiscarded, "unsupported turn", nil
	}
	evidence := f.b.toolByCall[callKey(f.meta.ID, callID)]
	if evidence == nil {
		f.batch = nil
		f.markUnsupported(line, fmt.Sprintf("tool output names unknown call %s", callID))
		return accountDiscarded, "unsupported tool output", nil
	}
	evidence.output = append(json.RawMessage(nil), output...)
	evidence.outputSeen = true
	evidence.outputLine = line
	f.trackAgentCall(evidence)
	f.batch = nil
	return accountMapped, "", nil
}

func (f *threadState) toolSearchOutput(line codextranscript.Line, output *codextranscript.ToolSearchOutput) (accountCategory, string, error) {
	category, reason, err := f.toolOutput(line, output.CallID, output.Tools)
	if err == nil {
		applyStatus(f.b.toolByCall[callKey(f.meta.ID, output.CallID)], output.Status)
	}
	return category, reason, err
}

func (f *threadState) mergeExecCompletion(line codextranscript.Line, completion *codextranscript.ExecCommandEnd) (accountCategory, string, error) {
	evidence := f.b.toolByCall[callKey(f.meta.ID, completion.CallID)]
	if evidence == nil {
		f.b.report.note("thread %s exec completion %s has no tool call", f.meta.ID, completion.CallID)
		return accountDiscarded, "context/orphan_exec_completion", nil
	}
	evidence.completions = append(evidence.completions, line)
	if command := commandText(completion.Command); command != "" {
		evidence.command = command
	}
	if completion.ExitCode != nil {
		if *completion.ExitCode == 0 {
			evidence.ok = true
		} else {
			evidence.error = true
		}
	}
	applyStatus(evidence, completion.Status)
	if completion.Stderr != "" && evidence.error {
		evidence.errorText = completion.Stderr
	}
	return accountMerged, "", nil
}

func (f *threadState) mergePatchCompletion(line codextranscript.Line, completion *codextranscript.PatchApplyEnd) (accountCategory, string, error) {
	evidence := f.b.toolByCall[callKey(f.meta.ID, completion.CallID)]
	if evidence == nil {
		if len(completion.Changes) == 0 {
			f.b.report.note("thread %s patch completion %s has no tool call", f.meta.ID, completion.CallID)
			return accountDiscarded, "context/orphan_patch_without_changes", nil
		}
		if f.activeInput == nil {
			f.hasOwner(line, "")
			return accountDiscarded, f.unsupportedReason(), nil
		}
		for _, path := range sortedPatchPaths(completion.Changes) {
			change := completion.Changes[path]
			f.addTouch(line, nil, f.activeInput, path, session.TouchEdit, change.UnifiedDiff)
		}
		f.b.report.note("thread %s patch completion %s produced file touches without a tool use", f.meta.ID, completion.CallID)
		return accountMapped, "", nil
	}
	evidence.completions = append(evidence.completions, line)
	if completion.Success != nil {
		if *completion.Success {
			evidence.ok = true
		} else {
			evidence.error = true
		}
	}
	applyStatus(evidence, completion.Status)
	if completion.Stderr != "" && evidence.error {
		evidence.errorText = completion.Stderr
	}
	for _, path := range sortedPatchPaths(completion.Changes) {
		change := completion.Changes[path]
		f.addTouch(line, evidence.use, nil, path, session.TouchEdit, change.UnifiedDiff)
	}
	return accountMerged, "", nil
}

func sortedPatchPaths(changes map[string]codextranscript.PatchChange) []string {
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (f *threadState) mergeMCPCompletion(line codextranscript.Line, completion *codextranscript.MCPToolCallEnd) (accountCategory, string, error) {
	evidence := f.b.toolByCall[callKey(f.meta.ID, completion.CallID)]
	if evidence == nil {
		f.b.report.note("thread %s MCP completion %s has no tool call", f.meta.ID, completion.CallID)
		return accountDiscarded, "context/orphan_mcp_completion", nil
	}
	evidence.completions = append(evidence.completions, line)
	var tagged map[string]json.RawMessage
	if json.Unmarshal(completion.Result, &tagged) == nil {
		if raw, ok := tagged["Err"]; ok {
			evidence.error = true
			evidence.errorText = outputText(raw)
			if evidence.errorText == "" {
				evidence.errorText = string(raw)
			}
			return accountMerged, "", nil
		}
		if _, ok := tagged["Ok"]; ok {
			evidence.ok = true
			return accountMerged, "", nil
		}
	}
	var result struct {
		IsError bool   `json:"is_error"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(completion.Result, &result) == nil {
		if result.IsError || result.Error != "" {
			evidence.error = true
			evidence.errorText = result.Error
		} else if _, hasSignal := tagged["is_error"]; hasSignal {
			evidence.ok = true
		}
	}
	return accountMerged, "", nil
}

func applyStatus(evidence *toolEvidence, status string) {
	if evidence == nil {
		return
	}
	switch {
	case terminalInterrupted(status):
		evidence.interrupted = true
	case terminalError(status):
		evidence.error = true
	case terminalOK(status):
		evidence.ok = true
	}
}

func terminalInterrupted(status string) bool {
	return oneOfFold(status, "interrupted", "cancelled", "canceled")
}

func terminalError(status string) bool {
	return oneOfFold(status, "error", "failed", "failure")
}

func terminalOK(status string) bool {
	return oneOfFold(status, "ok", "completed", "success", "succeeded")
}

func oneOfFold(status string, options ...string) bool {
	status = strings.ToLower(status)
	for _, option := range options {
		if status == option {
			return true
		}
	}
	return false
}

func (b *builder) finalizeTools() {
	for _, state := range b.order {
		for _, use := range state.tools {
			evidence := b.toolByID[use.ID]
			switch {
			case b.profile != traceprocessing.Hosted:
			case spawnTools[use.Name]:
				use.Metadata = session.WithSubagentDetail(use.Metadata, subagentDetail(evidence))
			case use.Name == "followup_task" && evidence != nil && evidence.report != "":
				// A follow-up that received the child's next report carries it.
				use.Metadata = session.WithSubagentDetail(use.Metadata, session.SubagentReport)
			}
			if evidence == nil || (!evidence.outputSeen && len(evidence.completions) == 0) {
				use.Status = session.StatusNoResult
				continue
			}
			status := session.StatusUnknown
			switch {
			case evidence.interrupted:
				status = session.StatusInterrupted
			case evidence.error:
				status = session.StatusError
			case evidence.ok:
				status = session.StatusOK
			}
			use.Status = status
			output := map[string]any{}
			if evidence.outputSeen {
				output["response_output"] = json.RawMessage(evidence.output)
			}
			if len(evidence.completions) > 0 {
				events := make([]json.RawMessage, len(evidence.completions))
				for i, completion := range evidence.completions {
					events[i] = completion.Payload
				}
				output["completion_events"] = events
			}
			outputRaw, _ := json.Marshal(output)
			row := &session.ToolResult{
				ID: state.ids.Mint("tool_result", evidence.callID), ToolUseID: use.ID,
				SessionID: use.SessionID, UserInputID: use.UserInputID, Status: status, Output: outputRaw,
			}
			if evidence.report != "" {
				report := evidence.report
				row.OutputText = &report
			} else if evidence.outputSeen {
				if text := outputText(evidence.output); text != "" {
					row.OutputText = &text
				}
				row.Timestamp = timePtr(evidence.outputLine.Timestamp)
			} else if len(evidence.completions) > 0 {
				row.Timestamp = timePtr(evidence.completions[len(evidence.completions)-1].Timestamp)
			}
			if evidence.errorText != "" {
				row.ErrorText = &evidence.errorText
			}
			state.results = append(state.results, row)
			ordinal := evidence.callLine.Ordinal
			if evidence.outputSeen {
				ordinal = evidence.outputLine.Ordinal
			} else if len(evidence.completions) > 0 {
				ordinal = evidence.completions[len(evidence.completions)-1].Ordinal
			}
			state.vendorRow(row.ID, session.EntityToolResult, "call_id", evidence.callID, ordinal)

			if status == session.StatusOK && use.Verb == session.VerbExecute && gitevidence.IsCommitCommand(evidence.command) && capturedCommitSHAFor(evidence, b.profile) {
				state.committed = true
			}
		}
	}
}

// capturedCommitSHA is whether the call's output or any of its completions
// names a commit. Each output is scanned on its own: a SHA never spans two.
func capturedCommitSHA(evidence *toolEvidence) bool {
	trust := gitevidence.TrustsOnelineLog(evidence.command)
	if len(gitevidence.CommitSHAs(outputText(evidence.output), trust)) > 0 {
		return true
	}
	for _, completion := range evidence.completions {
		end := completion.EventMsg.ExecCommandEnd
		if end != nil && (len(gitevidence.CommitSHAs(end.Stdout, trust)) > 0 || len(gitevidence.CommitSHAs(end.AggregatedOutput, trust)) > 0) {
			return true
		}
	}
	return false
}

func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var items []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) == nil {
		var parts []string
		for _, item := range items {
			parts = append(parts, item.Text)
		}
		return strings.Join(parts, "")
	}
	return ""
}

func commandText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []string
	if json.Unmarshal(raw, &parts) == nil {
		return strings.Join(parts, " ")
	}
	return outputText(raw)
}

func commandFromInput(input json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(input, &object) != nil {
		return ""
	}
	for _, key := range []string{"cmd", "command"} {
		if command := commandText(object[key]); command != "" {
			return command
		}
	}
	return ""
}

func capturedCommitSHAFor(evidence *toolEvidence, profile traceprocessing.MappingProfile) bool {
	if profile == traceprocessing.Hosted {
		return capturedCommitSHA(evidence)
	}
	captured := outputText(evidence.output)
	for _, completion := range evidence.completions {
		if end := completion.EventMsg.ExecCommandEnd; end != nil {
			captured += "\n" + end.Stdout + "\n" + end.AggregatedOutput
		}
	}
	return len(gitevidence.CommitSHAs(captured, gitevidence.TrustsOnelineLog(evidence.command))) > 0
}
