package codexsession

import (
	"encoding/json"
	"fmt"

	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

// applyTokenUsageRecord counts a token_usage_record's own usage once. From the
// first record on they are the thread's sole token source: the token_count that
// follows each one is discarded (see applyTokenCount), so no repeat check or
// thread total is involved. Totals drift apart after a remote compaction
// (the record's thread total includes the compaction request, token_count's does
// not), so comparing them would double count every later request.
func (f *threadState) applyTokenUsageRecord(line codextranscript.Line, record *codextranscript.TokenUsageRecord) (accountCategory, string, error) {
	f.b.report.Tokens.Records++
	f.seenTokenUsageRecord = true
	turn := f.usageTurn()
	if record.ThreadID != "" && record.ThreadID != f.meta.ID {
		f.b.report.note("token_usage_record at line %d names thread %s, not %s", line.Ordinal, record.ThreadID, f.meta.ID)
	}
	if record.TurnID != "" && (turn == nil || record.TurnID != turn.id) {
		f.b.report.note("token_usage_record at line %d names turn %s, not the turn it is attributed to", line.Ordinal, record.TurnID)
	}
	if turn == nil {
		return accountDiscarded, "context/token_outside_turn", nil
	}
	return f.assignUsage(line, turn, record.Usage)
}

// applyTokenCount takes a token_count event. Once the thread has seen a
// token_usage_record the records count every request, so the event is
// discarded. Before that a total equal to the thread's previous one is a repeat
// (Codex re-emits it, including at the start of the next turn).
func (f *threadState) applyTokenCount(line codextranscript.Line, count *codextranscript.TokenCount) (accountCategory, string, error) {
	if f.seenTokenUsageRecord {
		return accountDiscarded, "context/token_count_superseded", nil
	}
	f.b.report.Tokens.Records++
	if count.Info == nil {
		return accountDiscarded, "context/token_rate_limits", nil
	}
	turn := f.usageTurn()
	if turn == nil {
		return accountDiscarded, "context/token_outside_turn", nil
	}
	if f.lastTotal != nil && sameUsage(*f.lastTotal, count.Info.TotalTokenUsage) {
		f.b.report.Tokens.RepeatedTotals++
		return accountDiscarded, "context/token_repeat", nil
	}
	total := count.Info.TotalTokenUsage
	f.lastTotal = &total
	return f.assignUsage(line, turn, count.Info.LastTokenUsage)
}

// usageTurn is the turn a usage report belongs to: the open turn, or the last
// closed one when the report arrives after task_complete.
func (f *threadState) usageTurn() *turnState {
	turnID := f.openTurn
	if turnID == "" {
		turnID = f.lastClosedTurn
	}
	if turnID == "" {
		return nil
	}
	return f.turn(turnID)
}

// assignUsage gives one request's usage to the turn's single unaccounted
// invocation, or records it as an ambiguous session or input row.
func (f *threadState) assignUsage(line codextranscript.Line, turn *turnState, last codextranscript.Usage) (accountCategory, string, error) {
	if !usageReported(last) {
		turn.unaccounted = nil
		return accountDiscarded, "context/token_without_usage", nil
	}
	if len(turn.unaccounted) == 1 {
		applyInvocationUsage(turn.unaccounted[0], last)
		turn.unaccounted[0].Speed = speedForServiceTier(f.serviceTier)
		turn.unaccounted = nil
		f.b.report.Tokens.AssignedInvocations++
		return accountMerged, "", nil
	}

	one := int64(1)
	row := &session.TokenUsage{
		ID: f.ids.Mint("token_usage", fmt.Sprintf("%d", line.Ordinal)), SessionID: f.session.ID,
		Scope: session.UsageScopeSession, InputTokens: cloneInt64(last.InputTokens),
		OutputTokens: cloneInt64(last.OutputTokens), CacheReadTokens: cloneInt64(last.CachedInputTokens),
		CacheWriteTokens: cloneInt64(last.CacheWriteInputTokens), TotalTokens: cloneInt64(last.TotalTokens),
		// A token_count is one request's last_token_usage, whichever invocation
		// it belongs to, and the thread's tier at that point is known.
		APICallCount: cloneInt64(&one), Speed: speedForServiceTier(f.serviceTier),
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

// serviceTierOf reads service_tier from a thread_settings_applied snapshot.
// Every snapshot replaces the previous tier, so a snapshot without one clears it.
func serviceTierOf(settings json.RawMessage) string {
	var parsed struct {
		ServiceTier string `json:"service_tier"`
	}
	if json.Unmarshal(settings, &parsed) != nil {
		return ""
	}
	return parsed.ServiceTier
}

// speedForServiceTier maps Codex's service tier to an invocation speed. OpenAI's
// fast mode is requested as "fast" and, for GPT-5.6 and earlier, reported back
// as "priority" (its former name), so both are fast; "ultrafast" is its own,
// pricier tier; "default" and "standard" are standard. Anything else (flex, auto,
// or no tier ever set) is not reported. Codex reports no cache TTL.
func speedForServiceTier(tier string) *string {
	var speed string
	switch tier {
	case "priority", "fast":
		speed = session.SpeedFast
	case "ultrafast":
		speed = session.SpeedUltrafast
	case "default", "standard":
		speed = session.SpeedStandard
	default:
		return nil
	}
	return &speed
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
