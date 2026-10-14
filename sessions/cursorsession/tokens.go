package cursorsession

import (
	"encoding/json"
	"strings"

	"github.com/baumai/baum/sessions/cursortranscript"
)

type tokenEstimator struct {
	context int64
	limit   int64
}

func newTokenEstimator(data *cursortranscript.ComposerData, bubbles []*cursortranscript.Bubble) *tokenEstimator {
	floor, limit := composerTokenWindow(data)
	local := int64(0)
	for _, bubble := range bubbles {
		switch bubble.Type {
		case cursortranscript.BubbleTypeUser:
			local += estimateTokens(bubble.Text)
		case cursortranscript.BubbleTypeAssistant:
			output := estimateBubbleOutput(bubble)
			if bubble.TokenCount != nil && bubble.TokenCount.OutputTokens > 0 {
				output = bubble.TokenCount.OutputTokens
			}
			local += output + estimateToolResult(bubble)
		}
	}
	return &tokenEstimator{context: max(0, floor-local), limit: limit}
}

func (e *tokenEstimator) startInput(bubble *cursortranscript.Bubble) {
	e.context += estimateTokens(bubble.Text)
	if status := bubble.ContextWindowStatusAtCreation; status != nil {
		if status.TokensUsed > 0 {
			e.context = status.TokensUsed
		}
		if status.TokenLimit > 0 {
			e.limit = status.TokenLimit
		}
	}
	e.cap()
}

func (e *tokenEstimator) applyBatch(bubbles, reported []*cursortranscript.Bubble) (int64, int64) {
	input := e.context
	output := int64(0)
	toolResults := int64(0)
	for _, bubble := range bubbles {
		output += estimateBubbleOutput(bubble)
		toolResults += estimateToolResult(bubble)
	}
	if len(reported) == 1 {
		if reported[0].TokenCount.InputTokens > 0 {
			input = reported[0].TokenCount.InputTokens
		}
		if reported[0].TokenCount.OutputTokens > 0 {
			output = reported[0].TokenCount.OutputTokens
		}
	}
	e.context += output + toolResults
	e.cap()
	return max(0, input), max(0, output)
}

func (e *tokenEstimator) cap() {
	if e.limit > 0 {
		e.context = min(e.context, e.limit)
	}
	e.context = max(0, e.context)
}

func composerTokenWindow(data *cursortranscript.ComposerData) (int64, int64) {
	floor, limit := int64(0), int64(0)
	if data.ContextTokensUsed != nil {
		floor = *data.ContextTokensUsed
	}
	if data.ContextTokenLimit != nil {
		limit = *data.ContextTokenLimit
	}
	var breakdown struct {
		TotalUsedTokens int64 `json:"totalUsedTokens"`
		MaxTokens       int64 `json:"maxTokens"`
	}
	if json.Unmarshal(data.PromptTokenBreakdown, &breakdown) == nil {
		floor = max(floor, breakdown.TotalUsedTokens)
		limit = max(limit, breakdown.MaxTokens)
	}
	return max(0, floor), max(0, limit)
}

func estimateBubbleOutput(bubble *cursortranscript.Bubble) int64 {
	visible := []string{bubble.Text}
	if tool := wellFormedTool(bubble); tool != nil {
		visible = append(visible, tool.ToolName())
		raw := tool.RawArgs
		if len(nonNullRaw(raw)) == 0 {
			raw = tool.Params
		}
		visible = append(visible, sourceText(raw))
	}
	return estimateTokens(visible...) + estimateTokens(thinkingText(bubble.Thinking))
}

func estimateToolResult(bubble *cursortranscript.Bubble) int64 {
	if bubble.Tool == nil {
		return 0
	}
	return estimateTokens(sourceText(bubble.Tool.Result))
}

func thinkingText(raw json.RawMessage) string {
	if len(nonNullRaw(raw)) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var thinking struct {
		Text             string `json:"text"`
		RedactedThinking string `json:"redactedThinking"`
	}
	if json.Unmarshal(raw, &thinking) != nil {
		return ""
	}
	if thinking.Text != "" {
		return thinking.Text
	}
	return thinking.RedactedThinking
}

func estimateTokens(parts ...string) int64 {
	bytes := 0
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if bytes > 0 {
			bytes++
		}
		bytes += len(part)
	}
	if bytes == 0 {
		return 0
	}
	return int64((bytes + 3) / 4)
}
