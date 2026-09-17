package cursorsession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lastpersonlabs/reviewpal/sessions/cursortranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

func (f *composerState) addTool(input *session.UserInput, invocation *session.ModelInvocation, bubble *cursortranscript.Bubble) error {
	record := bubble.Tool
	if record.Degenerate() {
		f.b.report.note("composer %s bubble %s has a degenerate tool record", f.data.ComposerID, bubble.BubbleID)
		return nil
	}
	status, err := toolStatus(record.Status)
	if err != nil {
		return fmt.Errorf("bubble %s: %w", bubble.BubbleID, err)
	}
	inputRaw := toolInput(record)
	metadata := toolMetadata(record)
	verb := toolVerb(record.ToolName())
	params := decodedToolParams(inputRaw)
	if path, kind := touchPath(verb, params); path != "" && kind == session.TouchEdit {
		resolved := resolvedPath(f.workingDir, path)
		if resolved != path {
			metadata["pathResolution"] = map[string]string{
				"relativeWorkspacePath": path, "workingDir": f.workingDir, "resolvedPath": resolved,
			}
		}
	}
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	at, err := bubbleTime(bubble)
	if err != nil {
		return err
	}
	row := &session.ToolUse{
		ID: f.ids.Mint("tool_use", bubble.BubbleID, record.ToolCallID), SessionID: f.session.ID,
		UserInputID: input.ID, ModelInvocationID: invocation.ID, Name: record.ToolName(),
		Verb: verb, Input: inputRaw, Status: status, Metadata: metadataRaw, Timestamp: at,
	}
	f.tools = append(f.tools, row)
	f.toolByBubble[bubble.BubbleID] = &toolEvidence{bubble: bubble, row: row}
	kind, vendorID := "bubbleId", bubble.BubbleID
	if bubble.Text != "" {
		kind, vendorID = "bubbleId.block", bubble.BubbleID+":1"
	}
	f.vendorRow(row.ID, session.EntityToolUse, kind, vendorID, bubble.Key)
	f.vendorRow(row.ID, session.EntityToolUse, "toolCallId", record.ToolCallID, bubble.Key)

	if status == session.StatusInterrupted {
		input.Interrupted = true
	}
	if status != session.StatusNoResult {
		f.addToolResult(input, row, bubble, record, at)
	}
	f.addTouch(input, row, bubble, params)
	return nil
}

func toolStatus(status string) (session.ToolStatus, error) {
	switch status {
	case "completed":
		return session.StatusOK, nil
	case "error":
		return session.StatusError, nil
	case "cancelled":
		return session.StatusInterrupted, nil
	case "loading":
		return session.StatusNoResult, nil
	default:
		return "", fmt.Errorf("unknown tool status %q", status)
	}
}

func toolInput(record *cursortranscript.ToolRecord) json.RawMessage {
	if parsed, ok := parsedJSON(record.Params); ok {
		return parsed
	}
	if parsed, ok := parsedJSON(record.RawArgs); ok {
		return parsed
	}
	raw := record.Params
	if len(nonNullRaw(raw)) == 0 {
		raw = record.RawArgs
	}
	encoded, _ := json.Marshal(sourceText(raw))
	return encoded
}

func parsedJSON(raw json.RawMessage) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false
	}
	var inner string
	if json.Unmarshal(raw, &inner) == nil {
		trimmed := bytes.TrimSpace([]byte(inner))
		if !json.Valid(trimmed) {
			return nil, false
		}
		return append(json.RawMessage(nil), trimmed...), true
	}
	if json.Valid(raw) {
		return append(json.RawMessage(nil), raw...), true
	}
	return nil, false
}

func sourceText(raw json.RawMessage) string {
	var inner string
	if json.Unmarshal(raw, &inner) == nil {
		return inner
	}
	return string(raw)
}

func toolMetadata(record *cursortranscript.ToolRecord) map[string]any {
	metadata := map[string]any{"status": record.Status}
	if len(nonNullRaw(record.Tool)) > 0 {
		metadata["tool"] = record.Tool
	}
	if record.ToolIndex != nil {
		metadata["toolIndex"] = *record.ToolIndex
	}
	if record.ModelCallID != "" {
		metadata["modelCallId"] = record.ModelCallID
	}
	if record.UserDecision != "" {
		metadata["userDecision"] = record.UserDecision
	}
	if record.AdditionalData != nil && len(nonNullRaw(record.AdditionalData.Raw)) > 0 {
		metadata["additionalData"] = record.AdditionalData.Raw
	}
	return metadata
}

func (f *composerState) addToolResult(input *session.UserInput, use *session.ToolUse, bubble *cursortranscript.Bubble, record *cursortranscript.ToolRecord, at *time.Time) {
	row := &session.ToolResult{
		ID: f.ids.Mint("tool_result", bubble.BubbleID, record.ToolCallID), ToolUseID: use.ID,
		SessionID: f.session.ID, UserInputID: input.ID, Status: use.Status, Timestamp: at,
	}
	if len(nonNullRaw(record.Result)) > 0 {
		text := sourceText(record.Result)
		row.OutputText = &text
		if output, ok := parsedJSON(record.Result); ok {
			row.Output = output
		}
	}
	if len(nonNullRaw(record.Error)) > 0 {
		errorText := sourceText(record.Error)
		row.ErrorText = &errorText
	}
	f.results = append(f.results, row)
	kind, vendorID := "bubbleId", bubble.BubbleID
	if bubble.Text != "" {
		kind, vendorID = "bubbleId.block", bubble.BubbleID+":1"
	}
	f.vendorRow(row.ID, session.EntityToolResult, kind, vendorID, bubble.Key)
	f.vendorRow(row.ID, session.EntityToolResult, "toolCallId", record.ToolCallID, bubble.Key)
}
