package codexsession

import (
	"fmt"

	"github.com/lastpersonlabs/reviewpal/sessions/codextranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

func (f *threadState) applyTokenCount(line codextranscript.Line, count *codextranscript.TokenCount) (accountCategory, string, error) {
	f.b.report.Tokens.Records++
	if count.Info == nil {
		return accountDiscarded, "context/token_rate_limits", nil
	}
	turnID := f.openTurn
	if turnID == "" {
		turnID = f.lastClosedTurn
	}
	if turnID == "" {
		return accountDiscarded, "context/token_outside_turn", nil
	}
	turn := f.turn(turnID)
	if turn.lastTotal != nil && sameUsage(*turn.lastTotal, count.Info.TotalTokenUsage) {
		f.b.report.Tokens.RepeatedTotals++
		return accountDiscarded, "context/token_repeat", nil
	}
	total := count.Info.TotalTokenUsage
	turn.lastTotal = &total
	last := count.Info.LastTokenUsage
	if !usageReported(last) {
		turn.unaccounted = nil
		return accountDiscarded, "context/token_without_usage", nil
	}
	if len(turn.unaccounted) == 1 {
		applyInvocationUsage(turn.unaccounted[0], last)
		turn.unaccounted = nil
		f.b.report.Tokens.AssignedInvocations++
		return accountMerged, "", nil
	}

	row := &session.TokenUsage{
		ID: f.ids.Mint("token_usage", fmt.Sprintf("%d", line.Ordinal)), SessionID: f.session.ID,
		Scope: session.UsageScopeSession, InputTokens: cloneInt64(last.InputTokens),
		OutputTokens: cloneInt64(last.OutputTokens), CacheReadTokens: cloneInt64(last.CachedInputTokens),
		CacheWriteTokens: cloneInt64(last.CacheWriteInputTokens), TotalTokens: cloneInt64(last.TotalTokens),
	}
	owner := f.activeInput
	if owner == nil {
		owner = turn.lastInput
	}
	if owner != nil {
		row.Scope = session.UsageScopeInput
		row.UserInputID = &owner.ID
	}
	if model := commonInvocationModel(turn.unaccounted); model != nil {
		row.Model = model
	}
	f.usage = append(f.usage, row)
	f.vendorRow(row.ID, session.EntityTokenUsage, "rollout.line", fmt.Sprintf("%s:%d", f.meta.ID, line.Ordinal), line.Ordinal)
	turn.unaccounted = nil
	f.b.report.Tokens.AmbiguousRows++
	return accountMapped, "", nil
}

func commonInvocationModel(invocations []*session.ModelInvocation) *string {
	var model string
	for _, invocation := range invocations {
		if invocation.Model == nil || *invocation.Model == "" {
			return nil
		}
		if model != "" && model != *invocation.Model {
			return nil
		}
		model = *invocation.Model
	}
	if model == "" {
		return nil
	}
	return &model
}

func usageReported(usage codextranscript.Usage) bool {
	return usage.InputTokens != nil || usage.CachedInputTokens != nil || usage.CacheWriteInputTokens != nil ||
		usage.OutputTokens != nil || usage.TotalTokens != nil
}

func sameUsage(a, b codextranscript.Usage) bool {
	return sameInt64(a.InputTokens, b.InputTokens) &&
		sameInt64(a.CachedInputTokens, b.CachedInputTokens) &&
		sameInt64(a.CacheWriteInputTokens, b.CacheWriteInputTokens) &&
		sameInt64(a.OutputTokens, b.OutputTokens) &&
		sameInt64(a.ReasoningOutputTokens, b.ReasoningOutputTokens) &&
		sameInt64(a.TotalTokens, b.TotalTokens)
}

func sameInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func applyInvocationUsage(row *session.ModelInvocation, usage codextranscript.Usage) {
	row.InputTokens = int64Value(usage.InputTokens)
	row.OutputTokens = int64Value(usage.OutputTokens)
	row.CacheReadTokens = int64Value(usage.CachedInputTokens)
	row.CacheWriteTokens = int64Value(usage.CacheWriteInputTokens)
	row.TotalTokens = int64Value(usage.TotalTokens)
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
