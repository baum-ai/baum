package codexsession

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

var classDiscards = map[codextranscript.Class]string{
	codextranscript.ClassCompacted:  "context/compacted",
	codextranscript.ClassWorldState: "context/world_state",
	codextranscript.ClassRealtime:   "context/realtime_item",
}

var eventDiscards = map[string]string{
	"web_search_end":      "context/web_search_end",
	"agent_message":       "context/agent_message_event_copy",
	"agent_reasoning":     "context/agent_reasoning_event_copy",
	"context_compacted":   "context/compacted",
	"thread_rolled_back":  "context/thread_rolled_back",
	"item_completed":      "context/item_completed_copy",
	"thread_goal_updated": "context/thread_goal_updated",
}

func (f *threadState) process(line codextranscript.Line, next *codextranscript.Line) (accountCategory, string, error) {
	f.times.Observe(&line.Timestamp)
	if reason, ok := classDiscards[line.Class]; ok {
		return accountDiscarded, reason, nil
	}
	switch line.Class {
	case codextranscript.ClassSessionMeta:
		if line.Ordinal != 1 {
			return accountDiscarded, "context/session_meta", nil
		}
		f.applySessionMeta(line)
		return accountMapped, "", nil
	case codextranscript.ClassTurnContext:
		f.applyContext(line)
		return accountAttribute, "", nil
	case codextranscript.ClassResponseItem:
		return f.responseItem(line)
	case codextranscript.ClassEventMsg:
		return f.eventMsg(line)
	case codextranscript.ClassInterAgentMetadata:
		if next == nil || next.ResponseItem == nil || next.ResponseItem.AgentMessage == nil {
			return accountDiscarded, "", fmt.Errorf("inter_agent_communication_metadata is not immediately followed by an agent_message")
		}
		f.pendingInter = line.InterAgentMetadata
		return accountMerged, "", nil
	case codextranscript.ClassInterAgentCommunication:
		return f.interAgentCommunication(line)
	case codextranscript.ClassTokenUsageRecord:
		return f.applyTokenUsageRecord(line, line.TokenUsageRecord)
	default:
		return accountDiscarded, "", fmt.Errorf("unsupported record class %q", line.Class)
	}
}

func (f *threadState) responseItem(line codextranscript.Line) (accountCategory, string, error) {
	item := line.ResponseItem
	switch {
	case item.Message != nil:
		return f.responseMessage(line, item.Message)
	case item.AgentMessage != nil:
		return f.agentMessage(line, item.AgentMessage)
	case item.Reasoning != nil:
		if !f.hasOwner(line, "") {
			return accountDiscarded, f.unsupportedReason(), nil
		}
		f.startBatch(line)
		return accountDiscarded, "context/reasoning", nil
	case item.Compaction != nil:
		return accountDiscarded, "context/compaction_item", nil
	case item.ImageGeneration != nil:
		return accountDiscarded, "context/image_generation_call", nil
	case item.FunctionCall != nil:
		return f.ownedCall(line, item.FunctionCall.CallID, func() error {
			return f.addFunctionCall(line, item.FunctionCall)
		})
	case item.CustomCall != nil:
		return f.ownedCall(line, item.CustomCall.CallID, func() error {
			return f.addCustomCall(line, item.CustomCall)
		})
	case item.ToolSearchCall != nil:
		return f.ownedCall(line, item.ToolSearchCall.CallID, func() error {
			return f.addToolSearchCall(line, item.ToolSearchCall)
		})
	case item.WebSearchCall != nil:
		return f.ownedCall(line, "", func() error {
			return f.addWebSearchCall(line, item.WebSearchCall)
		})
	case item.FunctionOutput != nil:
		return f.toolOutput(line, item.FunctionOutput.CallID, item.FunctionOutput.Output)
	case item.CustomOutput != nil:
		return f.toolOutput(line, item.CustomOutput.CallID, item.CustomOutput.Output)
	case item.ToolSearchOutput != nil:
		return f.toolSearchOutput(line, item.ToolSearchOutput)
	default:
		return accountDiscarded, "", fmt.Errorf("response item %q has no decoded body", item.Type)
	}
}

func (f *threadState) responseMessage(line codextranscript.Line, message *codextranscript.Message) (accountCategory, string, error) {
	switch message.Role {
	case "assistant":
		if !f.hasOwner(line, "") {
			return accountDiscarded, f.unsupportedReason(), nil
		}
		if err := f.addAssistantMessage(line, message); err != nil {
			return accountDiscarded, "", err
		}
		return accountMapped, "", nil
	case "user":
		f.batch = nil
		return accountDiscarded, "context/model_visible_user_copy", nil
	case "developer", "system":
		return accountDiscarded, "context/" + message.Role + "_message", nil
	default:
		return accountDiscarded, "", fmt.Errorf("message has unknown role %q", message.Role)
	}
}

func (f *threadState) ownedCall(line codextranscript.Line, callID string, add func() error) (accountCategory, string, error) {
	if !f.hasOwner(line, callID) {
		return accountDiscarded, f.unsupportedReason(), nil
	}
	return accountMapped, "", add()
}

func messageText(message *codextranscript.Message, itemType string) (string, error) {
	items, err := message.Items()
	if err != nil {
		return "", fmt.Errorf("decoding %s message content: %w", message.Role, err)
	}
	var parts []string
	for _, item := range items {
		if item.Type == itemType {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, ""), nil
}

func (f *threadState) eventMsg(line codextranscript.Line) (accountCategory, string, error) {
	event := line.EventMsg
	switch {
	case event.TaskStarted != nil:
		return f.taskStarted(event.TaskStarted)
	case event.TaskComplete != nil:
		return f.endTurn(event.TaskComplete.TurnID, false)
	case event.TurnAborted != nil:
		turnID := event.TurnAborted.TurnID
		if turnID == "" {
			turnID = f.openTurn
		}
		return f.endTurn(turnID, true)
	case event.UserMessage != nil:
		if f.file != f.b.tree.Root {
			return accountDiscarded, "context/unconfirmed_human_input", nil
		}
		f.addHumanInput(line, event.UserMessage)
		return accountMapped, "", nil
	case event.ItemCompleted != nil:
		return f.itemCompleted(line, event.ItemCompleted)
	case event.TokenCount != nil:
		return f.applyTokenCount(line, event.TokenCount)
	case event.ExecCommandEnd != nil:
		return f.mergeExecCompletion(line, event.ExecCommandEnd)
	case event.PatchApplyEnd != nil:
		return f.mergePatchCompletion(line, event.PatchApplyEnd)
	case event.MCPToolCallEnd != nil:
		return f.mergeMCPCompletion(line, event.MCPToolCallEnd)
	case event.SubAgentActivity != nil:
		return f.subAgentActivity(line, *event.SubAgentActivity)
	case event.ThreadSettingsApplied != nil:
		f.addAttribute(line, "thread_settings", event.ThreadSettingsApplied.ThreadSettings)
		f.serviceTier = serviceTierOf(event.ThreadSettingsApplied.ThreadSettings)
		return accountAttribute, "", nil
	default:
		reason, ok := eventDiscards[event.Type]
		if !ok {
			reason = "context/event/" + event.Type
		}
		return accountDiscarded, reason, nil
	}
}

// noteUserMessageTurn records, for one owned line in file order, which turn
// a user_message event belongs to. One written before its task_started belongs
// to the turn that task_started opens, as pending input does.
func (f *threadState) noteUserMessageTurn(line codextranscript.Line) {
	event := line.EventMsg
	switch {
	case event == nil:
	case event.TaskStarted != nil:
		f.scanTurn = event.TaskStarted.TurnID
		if f.scanPending {
			f.userMessageTurns[f.scanTurn] = true
			f.scanPending = false
		}
	case event.TaskComplete != nil || event.TurnAborted != nil:
		f.scanTurn = ""
	case event.UserMessage != nil:
		if f.scanTurn == "" {
			f.scanPending = true
		} else {
			f.userMessageTurns[f.scanTurn] = true
		}
	}
}

// itemCompleted maps the human prompt that newer Codex versions record only as
// an item_completed UserMessage item, with no user_message event. In a turn
// that still writes user_message events every item_completed is a discarded
// copy, so no prompt counts twice. Other item types are discarded as before.
func (f *threadState) itemCompleted(line codextranscript.Line, completed *codextranscript.ItemCompleted) (accountCategory, string, error) {
	turnID := completed.TurnID
	if turnID == "" {
		turnID = f.openTurn
	}
	if f.userMessageTurns[turnID] {
		return accountDiscarded, eventDiscards["item_completed"], nil
	}
	var item struct {
		Type    string `json:"type"`
		Content []struct {
			Type         string          `json:"type"`
			Text         string          `json:"text"`
			Path         string          `json:"path"`
			ImageURL     string          `json:"image_url"`
			TextElements json.RawMessage `json:"text_elements"`
		} `json:"content"`
	}
	if err := json.Unmarshal(completed.Item, &item); err != nil {
		return accountDiscarded, "", fmt.Errorf("decoding item_completed item: %w", err)
	}
	if item.Type != "UserMessage" {
		return accountDiscarded, eventDiscards["item_completed"], nil
	}
	if f.file != f.b.tree.Root {
		return accountDiscarded, "context/unconfirmed_human_input", nil
	}
	var text strings.Builder
	message := &codextranscript.UserMessage{}
	var images, localImages, elements []json.RawMessage
	for _, part := range item.Content {
		switch part.Type {
		case "text":
			partElements, err := shiftTextElements(part.TextElements, text.Len())
			if err != nil {
				return accountDiscarded, "", fmt.Errorf("decoding item_completed text_elements: %w", err)
			}
			elements = append(elements, partElements...)
			text.WriteString(part.Text)
		case "image":
			images = append(images, quoted(part.ImageURL))
		case "local_image":
			localImages = append(localImages, quoted(part.Path))
		}
	}
	message.Message = text.String()
	message.Images, message.LocalImages, message.TextElements = rawArray(images), rawArray(localImages), rawArray(elements)
	f.addHumanInput(line, message)
	return accountMapped, "", nil
}

// shiftTextElements returns a text part's text_elements with each byte_range
// moved by offset, the byte length of the text before the part. Codex measures
// a range from the start of its own part, while the parts are joined into one
// message. A part without text_elements yields none; any other malformed value
// is an error.
func shiftTextElements(raw json.RawMessage, offset int) ([]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, err
	}
	for i, element := range elements {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(element, &fields); err != nil {
			return nil, err
		}
		if fields == nil {
			return nil, fmt.Errorf("text element is null")
		}
		rangeRaw, ok := fields["byte_range"]
		if !ok {
			continue
		}
		var byteRange struct {
			Start *int `json:"start"`
			End   *int `json:"end"`
		}
		if err := json.Unmarshal(rangeRaw, &byteRange); err != nil {
			return nil, err
		}
		if byteRange.Start == nil || byteRange.End == nil {
			return nil, fmt.Errorf("byte_range needs start and end")
		}
		if offset == 0 {
			continue
		}
		fields["byte_range"], _ = json.Marshal(map[string]int{"start": *byteRange.Start + offset, "end": *byteRange.End + offset})
		shifted, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		elements[i] = shifted
	}
	return elements, nil
}

func quoted(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

// rawArray is the JSON array of items, or nil when there are none, so
// addHumanInput stores no empty metadata.
func rawArray(items []json.RawMessage) json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	raw, _ := json.Marshal(items)
	return raw
}

func (f *threadState) taskStarted(started *codextranscript.TaskStarted) (accountCategory, string, error) {
	if started.TurnID == "" {
		return accountDiscarded, "", fmt.Errorf("task_started has no turn_id")
	}
	f.openTurn = started.TurnID
	f.turn(started.TurnID)
	f.activeInput, f.batch = nil, nil
	f.inputLine, f.batchNumber = 0, 0
	if len(f.pendingHuman) > 0 {
		for _, pending := range f.pendingHuman {
			f.inputTurn[pending.row.ID] = f.openTurn
		}
		pending := f.pendingHuman[len(f.pendingHuman)-1]
		f.activateInput(pending.row, pending.line)
		f.pendingHuman = nil
	}
	f.finalTurnAborted = false
	return accountDiscarded, "context/turn_lifecycle", nil
}

func (f *threadState) endTurn(turnID string, aborted bool) (accountCategory, string, error) {
	if err := f.closeTurn(turnID, aborted); err != nil {
		return accountDiscarded, "", err
	}
	return accountDiscarded, "context/turn_lifecycle", nil
}

func (f *threadState) subAgentActivity(line codextranscript.Line, activity codextranscript.SubAgentActivity) (accountCategory, string, error) {
	f.activities = append(f.activities, activityEvidence{line: line, activity: activity})
	evidence := f.b.toolByCall[callKey(f.meta.ID, activity.EventID)]
	if evidence == nil {
		return accountDiscarded, "context/unmatched_sub_agent_activity", nil
	}
	evidence.completions = append(evidence.completions, line)
	if activity.Kind == "started" {
		evidence.ok = true
	}
	return accountMerged, "", nil
}

func (f *threadState) turn(id string) *turnState {
	state := f.turns[id]
	if state == nil {
		state = &turnState{id: id}
		f.turns[id] = state
	}
	return state
}

func (f *threadState) closeTurn(id string, aborted bool) error {
	if f.openTurn == "" {
		return fmt.Errorf("turn %s ends without a matching task_started", id)
	}
	if f.openTurn != id {
		return fmt.Errorf("turn %s ends while turn %s is open", id, f.openTurn)
	}
	if aborted {
		for _, input := range f.inputs {
			if f.inputTurn[input.ID] == id {
				input.Interrupted = true
			}
		}
	}
	f.lastClosedTurn = id
	f.openTurn = ""
	f.activeInput = nil
	f.inputLine = 0
	f.batch = nil
	f.finalTurnAborted = aborted
	return nil
}

func (f *threadState) addHumanInput(line codextranscript.Line, message *codextranscript.UserMessage) {
	metadata := map[string]json.RawMessage{}
	for key, raw := range map[string]json.RawMessage{
		"images": message.Images, "local_images": message.LocalImages,
		"audio": message.Audio, "local_audio": message.LocalAudio, "text_elements": message.TextElements,
	} {
		if len(raw) > 0 && string(raw) != "null" && string(raw) != "[]" {
			metadata[key] = raw
		}
	}
	row := f.newInput(line, message.Message, session.KindPrompt, session.OriginHuman, metadata)
	if f.openTurn == "" {
		f.pendingHuman = append(f.pendingHuman, pendingHumanInput{row: row, line: line.Ordinal})
		return
	}
	f.activateInput(row, line.Ordinal)
}

func (f *threadState) addInput(line codextranscript.Line, text string, kind session.InputKind, origin session.InputOrigin, metadata map[string]json.RawMessage) {
	row := f.newInput(line, text, kind, origin, metadata)
	f.activateInput(row, line.Ordinal)
}

func (f *threadState) newInput(line codextranscript.Line, text string, kind session.InputKind, origin session.InputOrigin, metadata map[string]json.RawMessage) *session.UserInput {
	f.inputOrdinal++
	raw, _ := json.Marshal(metadata)
	row := &session.UserInput{
		ID: f.ids.Mint("user_input", fmt.Sprintf("%d", line.Ordinal)), SessionID: f.session.ID,
		Ordinal: f.inputOrdinal, Text: text, TriggeredAt: timePtr(line.Timestamp),
		Kind: kind, Origin: origin, Metadata: raw,
	}
	f.inputs = append(f.inputs, row)
	f.vendorRow(row.ID, session.EntityUserInput, "rollout.line", fmt.Sprintf("%s:%d", f.meta.ID, line.Ordinal), line.Ordinal)
	return row
}

func (f *threadState) activateInput(row *session.UserInput, line int) {
	f.inputTurn[row.ID] = f.openTurn
	f.turn(f.openTurn).lastInput = row
	f.activeInput, f.inputLine = row, line
	f.batch, f.batchNumber = nil, 0
}

func (f *threadState) agentMessage(line codextranscript.Line, message *codextranscript.AgentMessage) (accountCategory, string, error) {
	meta := f.pendingInter
	f.pendingInter = nil
	if meta == nil {
		return accountDiscarded, "context/agent_message_without_trigger", nil
	}
	f.batch = nil
	if !meta.TriggerTurn {
		return accountMerged, "", nil
	}
	metadata := map[string]json.RawMessage{
		"author": jsonString(message.Author), "recipient": jsonString(message.Recipient),
	}
	if len(message.OtherRecipients) > 0 {
		metadata["other_recipients"], _ = json.Marshal(message.OtherRecipients)
	}
	return f.addAgentInput(line, message.Recipient, message.Content, metadata)
}

func (f *threadState) interAgentCommunication(line codextranscript.Line) (accountCategory, string, error) {
	message := line.InterAgentCommunication
	if message.TriggerTurn == nil || !*message.TriggerTurn {
		f.batch = nil
		return accountMerged, "", nil
	}
	return f.addAgentInput(line, message.Recipient, message.Content, map[string]json.RawMessage{
		"author": jsonString(message.Author), "recipient": jsonString(message.Recipient),
	})
}

func (f *threadState) addAgentInput(line codextranscript.Line, recipient string, content json.RawMessage, metadata map[string]json.RawMessage) (accountCategory, string, error) {
	if f.openTurn == "" || recipient != f.meta.AgentPath {
		return accountDiscarded, "context/unconfirmed_agent_input", nil
	}
	text, err := joinedInputText(content)
	if err != nil {
		return accountDiscarded, "", err
	}
	f.addInput(line, text, session.KindOther, session.OriginAgent, metadata)
	return accountMapped, "", nil
}

func joinedInputText(raw json.RawMessage) (string, error) {
	var items []codextranscript.ContentItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", fmt.Errorf("decoding agent message content: %w", err)
	}
	var parts []string
	for _, item := range items {
		if item.Type == "input_text" && item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, ""), nil
}

func (f *threadState) hasOwner(line codextranscript.Line, callID string) bool {
	if f.activeInput != nil {
		return true
	}
	if callID != "" {
		f.unsupportedCall[callID] = true
	}
	f.markUnsupported(line, "consequences have no proven input")
	return false
}

func (f *threadState) markUnsupported(line codextranscript.Line, reason string) {
	scope := "turn:" + f.openTurn
	if f.openTurn == "" {
		scope = "outside-turn"
	}
	if f.unsupported[scope] {
		return
	}
	f.unsupported[scope] = true
	f.b.report.UnsupportedTurns++
	if f.openTurn == "" {
		f.b.report.note("thread %s line %d is unsupported outside a turn: %s", f.meta.ID, line.Ordinal, reason)
		return
	}
	f.b.report.note("thread %s turn %s is unsupported: %s", f.meta.ID, f.openTurn, reason)
}

func (f *threadState) unsupportedReason() string {
	if f.openTurn == "" {
		return "unsupported activity outside turn"
	}
	return "unsupported turn"
}

func jsonString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func timePtr(value time.Time) *time.Time {
	valueTime := value.UTC()
	return &valueTime
}
