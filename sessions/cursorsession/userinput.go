package cursorsession

import (
	"encoding/json"

	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func (f *composerState) addUserInput(bubble *cursortranscript.Bubble, ordinal int) (*session.UserInput, error) {
	at, err := bubbleTime(bubble)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{}
	if len(nonNullRaw(bubble.RichText)) > 0 {
		metadata["richText"] = true
	}
	if len(nonNullRaw(bubble.Context)) > 0 {
		metadata["context"] = bubble.Context
	}
	if bubble.CapabilityType != nil {
		metadata["capabilityType"] = *bubble.CapabilityType
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	kind, origin := session.KindPrompt, session.OriginHuman
	if f.session.ParentSessionID != nil {
		kind, origin = session.KindOther, session.OriginAgent
	}
	row := &session.UserInput{
		ID: f.ids.Mint("user_input", bubble.BubbleID), SessionID: f.session.ID,
		Ordinal: ordinal, Text: bubble.Text, TriggeredAt: at, Kind: kind, Origin: origin,
		Metadata: raw,
	}
	f.inputs = append(f.inputs, row)
	f.vendorRow(row.ID, session.EntityUserInput, "bubbleId", bubble.BubbleID, bubble.Key)
	if bubble.TokenCount.HasUsage() {
		f.addTokenUsage(bubble, row)
	}
	return row, nil
}

// addTokenUsage keeps a nonzero count that no single invocation owns. A nil
// input means no ask owns it either, so the row is session-scoped.
func (f *composerState) addTokenUsage(bubble *cursortranscript.Bubble, input *session.UserInput) {
	counts := bubble.TokenCount
	inputTokens, outputTokens := counts.InputTokens, counts.OutputTokens
	total := inputTokens + outputTokens
	row := &session.TokenUsage{
		ID: f.ids.Mint("token_usage", bubble.BubbleID), SessionID: f.session.ID,
		Scope:       session.UsageScopeSession,
		InputTokens: &inputTokens, OutputTokens: &outputTokens, TotalTokens: &total,
	}
	if model := tokenUsageModel(bubble, f.data); model != nil {
		row.Model = model
	}
	if input != nil {
		row.Scope = session.UsageScopeInput
		row.UserInputID = &input.ID
	}
	f.usage = append(f.usage, row)
	f.vendorRow(row.ID, session.EntityTokenUsage, "bubbleId", bubble.BubbleID, bubble.Key)
	f.b.report.Tokens.AmbiguousBubbles++
}

func tokenUsageModel(bubble *cursortranscript.Bubble, data *cursortranscript.ComposerData) *string {
	if bubble.ModelInfo != nil && bubble.ModelInfo.ModelName != "" {
		model := bubble.ModelInfo.ModelName
		return &model
	}
	if data.ModelConfig != nil && data.ModelConfig.ModelName != "" {
		model := data.ModelConfig.ModelName
		return &model
	}
	return nil
}
