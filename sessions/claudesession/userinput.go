package claudesession

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lastpersonlabs/reviewpal/sessions/claudetranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/prompttext"
	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

const interruptMarker = "[Request interrupted by user"

func (f *fileState) userLine(line claudetranscript.Line) error {
	produced := false

	switch line.UserKind {
	case claudetranscript.UserToolResults, claudetranscript.UserMixed:
		if err := f.toolResults(line); err != nil {
			return err
		}
		if line.UserKind == claudetranscript.UserMixed {
			f.addInput(line, line.Message.Text())
		}
		produced = true
	case claudetranscript.UserText, claudetranscript.UserOther:
		f.addInput(line, line.Message.Text())
		produced = true
	}

	if line.PromptID != "" {
		f.currentPrompt = line.PromptID
	}
	if produced {
		f.b.report.Lines.Mapped++
	} else {
		f.b.report.discard("user/" + string(line.UserKind))
	}
	return nil
}

// addInput records one ask. Every user-triggered record becomes its own row.
func (f *fileState) addInput(line claudetranscript.Line, text string) {
	kind := classifyInput(line, text)
	if kind == session.KindPrompt && f.mergeOrdinaryCarrier(line, text, kind) {
		return
	}

	if kind == session.KindInterrupt {
		f.markInterrupted(line)
	}

	f.ordinal++
	input := &session.UserInput{
		ID:          f.ids.Mint("user_input", line.UUID),
		SessionID:   f.sessionID,
		Ordinal:     f.ordinal,
		Text:        text,
		TriggeredAt: line.Timestamp,
		Kind:        kind,
		Origin:      f.inputOrigin(kind),
		Metadata:    inputMetadata(line),
	}
	f.inputs = append(f.inputs, input)
	f.vendorRow(input.ID, session.EntityUserInput, "uuid", line.UUID, line.Ordinal)

	if kind != session.KindInterrupt {
		f.lastAttachable = input
	}
	f.claimPrompt(line.PromptID, input, kind, text, line.Ordinal)

	if kind != session.KindInterrupt && strings.Contains(text, interruptMarker) {
		input.Interrupted = true
	}
}

func (f *fileState) inputOrigin(kind session.InputKind) session.InputOrigin {
	switch kind {
	case session.KindOther:
		return session.OriginSystem
	case session.KindPrompt:
		if f.session.ParentSessionID != nil {
			return session.OriginAgent
		}
	}
	return session.OriginHuman
}

// claimPrompt decides which ask owns a prompt id.
func (f *fileState) claimPrompt(promptID string, input *session.UserInput, kind session.InputKind, text string, ordinal int) {
	if promptID == "" {
		return
	}
	_, seen := f.owners[promptID]
	substantive := kind != session.KindInterrupt && !prompttext.IsBoilerplate(text)

	if !seen || (substantive && f.placeholders[promptID]) {
		f.owners[promptID] = input.ID
		f.promptOrdinal[promptID] = ordinal
		f.placeholders[promptID] = !substantive
		if !seen {
			f.promptOrder = append(f.promptOrder, promptID)
		}
	}
}

// markInterrupted flags the ask an interrupt cut short.
func (f *fileState) markInterrupted(line claudetranscript.Line) {
	if id, owned := f.owners[line.PromptID]; owned {
		for _, input := range f.inputs {
			if input.ID == id {
				input.Interrupted = true
				return
			}
		}
	}
	if f.lastAttachable != nil {
		f.lastAttachable.Interrupted = true
	}
}

// classifyInput decides what kind of ask a user record is.
func classifyInput(line claudetranscript.Line, text string) session.InputKind {
	stripped := prompttext.Strip(text)

	switch {
	case strings.Contains(text, "<command-name>"):
		return session.KindSlashCommand
	case stripped == "" && strings.Contains(text, interruptMarker):
		return session.KindInterrupt
	case stripped == "":
		return session.KindOther
	case injected(line, text):
		return session.KindOther
	default:
		return session.KindPrompt
	}
}

// injected reports whether the harness wrote this text rather than a person.
func injected(line claudetranscript.Line, text string) bool {
	if line.PromptSource == "system" {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(text), "<task-notification>") {
		return true
	}
	var origin struct {
		Kind string `json:"kind"`
	}
	if len(line.Origin) > 0 && json.Unmarshal(line.Origin, &origin) == nil {
		return origin.Kind == "task-notification"
	}
	return false
}

// inputMetadata is the vendor's own fields under the vendor's own names.
func inputMetadata(line claudetranscript.Line) json.RawMessage {
	bag := map[string]any{}
	if line.PromptID != "" {
		bag["promptId"] = line.PromptID
	}
	if line.PromptSource != "" {
		bag["promptSource"] = line.PromptSource
	}
	if line.ToolDenialKind != "" {
		bag["toolDenialKind"] = line.ToolDenialKind
	}
	if line.IsMeta {
		bag["isMeta"] = true
	}
	if len(line.Origin) > 0 {
		bag["origin"] = line.Origin
	}

	raw, err := json.Marshal(bag)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// toolResults maps every tool_result block on a user line.
func (f *fileState) toolResults(line claudetranscript.Line) error {
	evidence := claudetranscript.DecodeResultEvidence(line.ToolUseResult)

	for _, block := range line.Message.Blocks() {
		if block.Type != "tool_result" {
			continue
		}
		use, known := f.byToolUseID[block.ToolUseID]
		if !known {
			return fmt.Errorf("tool result names tool use %s, which this transcript does not contain", block.ToolUseID)
		}
		f.applyResult(line, block, evidence, use)
	}
	return nil
}
