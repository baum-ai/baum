package cursorsession

import (
	"fmt"

	"github.com/lastpersonlabs/reviewpal/sessions/cursortranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

type bubbleBatch struct {
	number  int
	bubbles []*cursortranscript.Bubble
}

func (f *composerState) processBatches(group *ownedInput) error {
	var previousInvocation string
	f.estimator.startInput(group.bubble)
	for _, batch := range splitBatches(group.bubbles) {
		locator := fmt.Sprintf("%s:%d:%d", f.data.ComposerID, group.row.Ordinal, batch.number)
		startedAt, err := bubbleTime(batch.bubbles[0])
		if err != nil {
			return err
		}
		row := &session.ModelInvocation{
			ID: f.ids.Mint("model_invocation", locator), SessionID: f.session.ID,
			UserInputID: group.row.ID, StartedAt: startedAt,
		}
		if previousInvocation != "" {
			parent := previousInvocation
			row.ParentModelInvocationID = &parent
		}
		if model := batchModel(batch.bubbles, f.data); model != "" {
			row.Model = &model
		}
		var usage []*cursortranscript.Bubble
		for _, bubble := range batch.bubbles {
			if bubble.TokenCount.HasUsage() {
				usage = append(usage, bubble)
			}
		}
		estimatedInput, estimatedOutput := f.estimator.applyBatch(batch.bubbles, usage)
		if len(usage) == 1 {
			row.InputTokens = usage[0].TokenCount.InputTokens
			row.OutputTokens = usage[0].TokenCount.OutputTokens
			row.TotalTokens = row.InputTokens + row.OutputTokens
			f.b.report.Tokens.AssignedInvocations++
		} else if len(usage) > 1 {
			for _, bubble := range usage {
				f.addTokenUsage(bubble, group.row)
			}
		} else {
			row.InputTokens = estimatedInput
			row.OutputTokens = estimatedOutput
			row.TotalTokens = estimatedInput + estimatedOutput
			row.TokenBasis = session.TokenBasisEstimated
			f.b.report.Tokens.EstimatedInvocations++
		}
		f.invocations = append(f.invocations, row)
		f.vendorRow(row.ID, session.EntityModelInvocation, "batch", locator, batch.bubbles[0].Key)
		previousInvocation = row.ID

		for _, bubble := range batch.bubbles {
			if bubble.Text != "" {
				if err := f.addAssistantMessage(group.row, row, bubble); err != nil {
					return err
				}
			}
			if bubble.Tool != nil {
				if err := f.addTool(group.row, row, bubble); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func splitBatches(bubbles []*cursortranscript.Bubble) []bubbleBatch {
	var out []bubbleBatch
	for _, bubble := range bubbles {
		newBatch := len(out) == 0
		if !newBatch {
			previous := out[len(out)-1].bubbles[len(out[len(out)-1].bubbles)-1]
			previousTool, currentTool := wellFormedTool(previous), wellFormedTool(bubble)
			switch {
			case previousTool != nil && currentTool == nil:
				newBatch = true
			case previousTool != nil && currentTool != nil &&
				previousTool.ModelCallID != "" && currentTool.ModelCallID != "" &&
				previousTool.ModelCallID != currentTool.ModelCallID:
				newBatch = true
			}
		}
		if newBatch {
			out = append(out, bubbleBatch{number: len(out) + 1})
		}
		out[len(out)-1].bubbles = append(out[len(out)-1].bubbles, bubble)
	}
	return out
}

// wellFormedTool returns the bubble's tool record for batch-boundary decisions;
// a degenerate record is not a tool call and must not split or extend a batch.
func wellFormedTool(bubble *cursortranscript.Bubble) *cursortranscript.ToolRecord {
	if bubble.Tool == nil || bubble.Tool.Degenerate() {
		return nil
	}
	return bubble.Tool
}

func batchModel(bubbles []*cursortranscript.Bubble, data *cursortranscript.ComposerData) string {
	var model string
	for _, bubble := range bubbles {
		if bubble.ModelInfo != nil && bubble.ModelInfo.ModelName != "" {
			model = bubble.ModelInfo.ModelName
		}
	}
	if model == "" && data.ModelConfig != nil {
		model = data.ModelConfig.ModelName
	}
	return model
}

func (f *composerState) addAssistantMessage(input *session.UserInput, invocation *session.ModelInvocation, bubble *cursortranscript.Bubble) error {
	at, err := bubbleTime(bubble)
	if err != nil {
		return err
	}
	text := bubble.Text
	row := &session.AssistantMessage{
		ID: f.ids.Mint("assistant_message", bubble.BubbleID), SessionID: f.session.ID,
		UserInputID: input.ID, ModelInvocationID: invocation.ID, Text: &text, Timestamp: at,
	}
	f.messages = append(f.messages, row)
	kind, vendorID := "bubbleId", bubble.BubbleID
	if bubble.Tool != nil {
		kind, vendorID = "bubbleId.block", bubble.BubbleID+":0"
	}
	f.vendorRow(row.ID, session.EntityAssistantMessage, kind, vendorID, bubble.Key)
	return nil
}
