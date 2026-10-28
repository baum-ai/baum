package claudesession

import (
	"encoding/json"
	"strings"

	"github.com/baum-ai/baum/linkage/gitevidence"
	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/session"
)

// policyDenials are refusals that came from a rule rather than from a person.
var policyDenials = map[string]bool{
	"permission-rule":      true,
	"automode-blocked":     true,
	"automode-unavailable": true,
}

// applyResult records the outcome of one tool call and copies it onto the call.
func (f *fileState) applyResult(
	line claudetranscript.Line,
	block claudetranscript.Block,
	evidence claudetranscript.ResultEvidence,
	use *session.ToolUse,
) {
	text := block.ResultText()
	status := resultStatus(line, block, evidence, text)

	row := &session.ToolResult{
		ID:          f.ids.Mint("tool_result", block.ToolUseID),
		ToolUseID:   use.ID,
		SessionID:   f.sessionID,
		UserInputID: use.UserInputID,
		Status:      status,
		Timestamp:   line.Timestamp,
	}
	if evidence.Present {
		row.Output = evidence.Raw
	}
	assignResultText(row, block, evidence, text)

	if existing, seen := f.resultByTool[block.ToolUseID]; seen {
		*existing = *row
		row = existing
	} else {
		f.resultByTool[block.ToolUseID] = row
		f.results = append(f.results, row)
		f.vendorRow(row.ID, session.EntityToolResult, "tool_use_id", block.ToolUseID, line.Ordinal)
	}

	use.Status = status
	f.lastToolStatus = status
	f.trackSubagentCall(use, evidence, row)
	f.refineTouches(use, evidence)
	f.noteCommit(use, evidence, text, status)
}

// assignResultText splits the payload into the readable output and the error.
func assignResultText(
	row *session.ToolResult,
	block claudetranscript.Block,
	evidence claudetranscript.ResultEvidence,
	text string,
) {
	output := text
	if output == "" {
		output = evidence.Stdout
	}
	if output == "" {
		output = evidence.Text
	}

	errorText := evidence.Stderr
	failed := block.IsError != nil && *block.IsError
	if errorText == "" && failed {
		errorText, output = output, ""
	}

	if output != "" {
		row.OutputText = &output
	}
	if errorText != "" {
		row.ErrorText = &errorText
	}
}

// resultStatus maps Claude's signals onto our portable outcome.
func resultStatus(
	line claudetranscript.Line,
	block claudetranscript.Block,
	evidence claudetranscript.ResultEvidence,
	text string,
) session.ToolStatus {
	switch {
	case evidence.Interrupted,
		line.ToolDenialKind == "user-rejected",
		strings.Contains(text, interruptMarker),
		strings.Contains(evidence.Text, interruptMarker):
		return session.StatusInterrupted
	case block.IsError != nil && *block.IsError,
		evidence.ExitCode != nil && *evidence.ExitCode != 0,
		policyDenials[line.ToolDenialKind]:
		return session.StatusError
	default:
		return session.StatusOK
	}
}

// noteCommit records that this session committed something.
func (f *fileState) noteCommit(
	use *session.ToolUse,
	evidence claudetranscript.ResultEvidence,
	text string,
	status session.ToolStatus,
) {
	if f.committed || status != session.StatusOK || use.Verb != session.VerbExecute {
		return
	}
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(use.Input, &input); err != nil || !gitevidence.IsCommitCommand(input.Command) {
		return
	}
	trust := gitevidence.TrustsOnelineLog(input.Command)
	if len(gitevidence.CommitSHAs(evidence.Stdout, trust)) > 0 || len(gitevidence.CommitSHAs(text, trust)) > 0 {
		f.committed = true
	}
}
