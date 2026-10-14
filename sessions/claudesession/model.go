package claudesession

import (
	"encoding/json"
	"fmt"

	"github.com/baumai/baum/sessions/claudetranscript"
	"github.com/baumai/baum/sessions/session"
)

// assistantLine maps one `assistant` line into the reply it belongs to.
func (f *fileState) assistantLine(line claudetranscript.Line) error {
	owner, err := f.ownerInput()
	if err != nil {
		return err
	}

	invocation := f.invocation(line, owner)
	for index, block := range line.Message.Blocks() {
		if block.Type == "tool_use" {
			if err := f.addToolUse(line, block, index, invocation); err != nil {
				return err
			}
			continue
		}
		f.addMessage(line, block, index, invocation)
	}
	f.b.report.Lines.Mapped++
	return nil
}

// invocation returns the reply this line belongs to, creating it on first sight.
func (f *fileState) invocation(line claudetranscript.Line, ownerInput string) *session.ModelInvocation {
	existing, known := f.byMessageID[line.Message.ID]
	if known {
		f.applyUsage(existing, line)
		return existing
	}

	row := &session.ModelInvocation{
		ID:          f.ids.Mint("model_invocation", line.Message.ID),
		SessionID:   f.sessionID,
		UserInputID: ownerInput,
		StartedAt:   line.Timestamp,
	}
	if previous, chained := f.lastByInput[ownerInput]; chained {
		row.ParentModelInvocationID = &previous
	}
	f.lastByInput[ownerInput] = row.ID

	f.applyUsage(row, line)
	f.byMessageID[line.Message.ID] = row
	f.invocations = append(f.invocations, row)

	f.vendorRow(row.ID, session.EntityModelInvocation, "message.id", line.Message.ID, line.Ordinal)
	f.vendorRow(row.ID, session.EntityModelInvocation, "requestId", line.RequestID, line.Ordinal)
	f.vendorRow(row.ID, session.EntityModelInvocation, "uuid", line.UUID, line.Ordinal)
	return row
}

// applyUsage overwrites the reply's accounting from this line, which is how
// last-line-wins is implemented.
func (f *fileState) applyUsage(row *session.ModelInvocation, line claudetranscript.Line) {
	if model := line.Message.Model; model != "" {
		row.Model = &model
	}
	if reason := line.Message.StopReason; reason != nil && *reason != "" {
		value := *reason
		row.StopReason = &value
	}
	usage := line.Message.Usage
	if usage == nil {
		return
	}
	row.InputTokens = usage.InputTokens
	row.OutputTokens = usage.OutputTokens
	row.CacheReadTokens = usage.CacheReadInputTokens
	row.CacheWriteTokens = usage.CacheCreationInputTokens
	// The four buckets are disjoint, so the total is their sum.
	row.TotalTokens = usage.InputTokens + usage.OutputTokens +
		usage.CacheReadInputTokens + usage.CacheCreationInputTokens

	// The 1-hour part is recorded only when it fits inside the total. When 1h +
	// 5m differs from cache_creation_input_tokens we do not guess which figure is
	// wrong: a 1h figure that fits is kept, anything else is left unreported.
	row.CacheWrite1hTokens = nil
	if h := usage.CacheWrite1h; h != nil && *h >= 0 && *h <= usage.CacheCreationInputTokens {
		value := *h
		row.CacheWrite1hTokens = &value
	}
	row.Speed = nil
	if session.SpeedValid(usage.Speed) {
		speed := usage.Speed
		row.Speed = &speed
	}
}

// addMessage records one block of model output. Every block that is not a tool
// call becomes a message, thinking included
func (f *fileState) addMessage(line claudetranscript.Line, block claudetranscript.Block, index int, invocation *session.ModelInvocation) {
	row := &session.AssistantMessage{
		ID:                f.ids.Mint("assistant_message", line.UUID, fmt.Sprint(index)),
		SessionID:         f.sessionID,
		UserInputID:       invocation.UserInputID,
		ModelInvocationID: invocation.ID,
		Content:           block.Raw,
		Timestamp:         line.Timestamp,
	}
	if block.Text != "" {
		text := block.Text
		row.Text = &text
	}
	f.messages = append(f.messages, row)
	f.blockVendorRow(row.ID, session.EntityAssistantMessage, "uuid", line.UUID, index, line.Ordinal)
}

func (f *fileState) addToolUse(
	line claudetranscript.Line,
	block claudetranscript.Block,
	index int,
	invocation *session.ModelInvocation,
) error {
	if block.ID == "" {
		return fmt.Errorf("a tool call has no id")
	}
	spawned := f.b.tree.SubagentForToolUse(block.ID)

	// Arguments are an object on every real call. Anything else cannot be stored.
	input := jsonObjectOr(block.Input)
	if len(block.Input) > 0 && block.Input[0] != '{' {
		f.b.report.note("tool call %s (%s) has arguments that are not an object and were not stored", block.ID, block.Name)
	}

	row := &session.ToolUse{
		ID:                f.ids.Mint("tool_use", block.ID),
		SessionID:         f.sessionID,
		UserInputID:       invocation.UserInputID,
		ModelInvocationID: invocation.ID,
		Name:              block.Name,
		Verb:              verbFor(block.Name, spawned != nil),
		Input:             input,
		Status:            session.StatusNoResult,
		Metadata:          spawnMetadata(spawned),
		Timestamp:         line.Timestamp,
	}
	f.toolUses = append(f.toolUses, row)
	f.byToolUseID[block.ID] = row
	f.toolUseVendor[row.ID] = block.ID
	f.vendorRow(row.ID, session.EntityToolUse, "tool_use.id", block.ID, line.Ordinal)
	f.blockVendorRow(row.ID, session.EntityToolUse, "uuid", line.UUID, index, line.Ordinal)

	if spawned != nil {
		f.spawns = append(f.spawns, spawnedTranscript{vendorToolUseID: block.ID, ourToolUseID: row.ID})
	}
	f.addTouchesFromInput(row, block)
	return nil
}

// spawnMetadata carries the subagent sidecar onto the call that spawned it.
func spawnMetadata(spawned *claudetranscript.File) json.RawMessage {
	if spawned == nil || spawned.Meta == nil {
		return json.RawMessage(`{}`)
	}
	bag := map[string]any{"agentId": spawned.AgentID, "spawnDepth": spawned.Meta.SpawnDepth}
	for key, value := range map[string]string{
		"agentType":     spawned.Meta.AgentType,
		"description":   spawned.Meta.Description,
		"parentAgentId": spawned.Meta.ParentAgentID,
		"model":         spawned.Meta.Model,
	} {
		if value != "" {
			bag[key] = value
		}
	}
	raw, err := json.Marshal(bag)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
