package claudesession

import (
	"fmt"

	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

// Report says what a transcript tree became, and what it did not.
type Report struct {
	Sessions   int                    `json:"sessions"`
	Rows       Rows                   `json:"rows"`
	Lines      Lines                  `json:"lines"`
	Transcript claudetranscript.Shape `json:"transcript"`
	Notes      []string               `json:"notes,omitempty"`
}

type Rows struct {
	UserInputs        int `json:"user_inputs"`
	ModelInvocations  int `json:"model_invocations"`
	AssistantMessages int `json:"assistant_messages"`
	ToolUses          int `json:"tool_uses"`
	ToolResults       int `json:"tool_results"`
	FileTouches       int `json:"file_touches"`
	Attributes        int `json:"session_attributes"`
	VendorIDs         int `json:"vendor_ids"`
}

func rowsFrom(t traceprocessing.RowTally) Rows {
	return Rows{t.UserInputs, t.ModelInvocations, t.AssistantMessages, t.ToolUses, t.ToolResults, t.FileTouches, t.Attributes, t.VendorIDs}
}

// Lines accounts for the lines read, one bucket each.
type Lines = traceprocessing.LineAccounting

func (r *Report) discard(reason string) { r.discardN(reason, 1) }

func (r *Report) discardN(reason string, lines int) {
	traceprocessing.AddDiscard(&r.Lines.Discarded, reason, lines)
}

func (r *Report) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}
