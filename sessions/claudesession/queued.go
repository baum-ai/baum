package claudesession

import (
	"encoding/json"
	"fmt"
	"github.com/baum-ai/baum/sessions/traceprocessing"
	"time"

	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/session"
)

// queuedInput is one queue identity while the graph is being built.
// This is mostly due to the way claude can queue user messages etc.
type queuedInput struct {
	row               *session.UserInput
	operations        []any
	attachmentSeen    bool
	terminalOperation bool
	normalSeen        bool
}

// recordQueuedOperation folds one queue-operation line into its queue identity,
// creating the row on `enqueue` and activating it on any other operation that
// reaches here.
func (f *fileState) recordQueuedOperation(line claudetranscript.Line) bool {
	text := line.Control.Content
	var fallback *queuedInput
	if line.Control.Operation != "enqueue" {
		fallback = f.latestQueued(text, func(q *queuedInput) bool {
			return !q.terminalOperation && !q.normalSeen
		})
	}
	queued, created := f.findOrCreateQueued(
		queueIdentity(line.Timestamp, line.Ordinal, text),
		text, line.Timestamp, fallback,
	)
	f.addQueueOperationMetadata(queued, line.Control.Operation, line.Timestamp, line.Ordinal)
	if line.Control.Operation != "enqueue" {
		queued.terminalOperation = true
		f.activateQueued(queued)
	}
	return created
}

// mergeOrdinaryCarrier folds a later ordinary `user` row into the queue-derived
// input as evidence (uuid, prompt id, metadata) without replacing that input.
func (f *fileState) mergeOrdinaryCarrier(line claudetranscript.Line, text string, kind session.InputKind) bool {
	queued := f.matchQueuedInput(line.Timestamp, text)
	if queued == nil || queued.normalSeen {
		return false
	}
	queued.normalSeen = true
	if f.b.profile == traceprocessing.Hosted && kind == session.KindOther {
		// The row says another agent sent it, which the queue alone may not.
		queued.row.Kind, queued.row.Origin = session.KindOther, session.OriginSystem
	}
	f.activateQueued(queued)
	f.mergeQueuedMetadata(queued, inputMetadata(line))
	f.vendorRow(queued.row.ID, session.EntityUserInput, "uuid", line.UUID, line.Ordinal)
	f.claimPrompt(line.PromptID, queued.row, kind, text, line.Ordinal)
	return true
}

func (f *fileState) attachmentLine(line claudetranscript.Line) {
	attachment := line.Attachment
	if attachment.Type != "queued_command" {
		f.b.report.discard("attachment")
		return
	}
	if attachment.Prompt == "" {
		f.b.report.discard("attachment/queued_command (no prompt)")
		return
	}
	if _, notification := parseNotification(attachment.Prompt); notification || !queuedPromptAttachment(attachment) {
		f.b.report.discard("attachment/queued_command (non-human)")
		return
	}

	at := attachment.Timestamp
	if at == nil {
		at = line.Timestamp
	}
	// Source order is only a fallback when the attachment timestamp does not
	// reproduce the queue identity. Distinct attachment carriers stay distinct.
	queued, created := f.findOrCreateQueued(
		queueIdentity(at, line.Ordinal, attachment.Prompt),
		attachment.Prompt, at,
		f.latestQueued(attachment.Prompt, func(q *queuedInput) bool {
			return !q.attachmentSeen && !q.normalSeen
		}),
	)
	if f.b.profile == traceprocessing.Hosted && peerOrigin(attachment.Origin) {
		queued.row.Kind, queued.row.Origin = session.KindOther, session.OriginSystem
	}
	f.addQueuedAttachmentMetadata(queued, attachment)
	f.vendorRow(queued.row.ID, session.EntityUserInput, "uuid", line.UUID, line.Ordinal)
	queued.attachmentSeen = true
	f.activateQueued(queued)
	if created {
		f.b.report.Lines.Mapped++
	} else {
		f.b.report.Lines.Merged++
	}
}

// queuedPromptAttachment is whether a queued command is queued as a prompt:
// one a person typed, or another agent's message queued the same way.
func queuedPromptAttachment(attachment claudetranscript.Attachment) bool {
	return attachment.CommandMode == "prompt" || originKind(attachment.Origin) == "human"
}

func (f *fileState) findOrCreateQueued(identity, text string, at *time.Time, fallback *queuedInput) (*queuedInput, bool) {
	if queued := f.queuedByIdentity[identity]; queued != nil {
		return queued, false
	}
	if fallback != nil {
		return fallback, false
	}
	return f.addQueuedInput(identity, text, at), true
}

func (f *fileState) addQueuedInput(identity, text string, at *time.Time) *queuedInput {
	f.ordinal++
	row := &session.UserInput{
		ID: f.ids.Mint("user_input", "queued", identity), SessionID: f.sessionID,
		Ordinal: f.ordinal, Text: text, TriggeredAt: at,
		Kind: session.KindPrompt, Origin: session.OriginHuman,
		Metadata: json.RawMessage(`{"queued":true}`),
	}
	if _, _, fromAgent := agentMessage(text); f.b.profile == traceprocessing.Hosted && fromAgent {
		// Another agent's message, a subagent's hand-back or a SendMessage, is
		// queued like a prompt, but no person wrote it.
		row.Kind, row.Origin = session.KindOther, session.OriginSystem
	}
	queued := &queuedInput{row: row}
	f.inputs = append(f.inputs, row)
	f.queuedByIdentity[identity] = queued
	f.queuedByText[text] = append(f.queuedByText[text], queued)
	return queued
}

func (f *fileState) latestQueued(text string, ok func(*queuedInput) bool) *queuedInput {
	for i := len(f.queuedByText[text]) - 1; i >= 0; i-- {
		if q := f.queuedByText[text][i]; ok(q) {
			return q
		}
	}
	return nil
}

func (f *fileState) matchQueuedInput(at *time.Time, text string) *queuedInput {
	if queued := f.queuedByIdentity[queueIdentity(at, 0, text)]; queued != nil {
		return queued
	}
	return f.latestQueued(text, func(q *queuedInput) bool { return !q.normalSeen })
}

func (f *fileState) activateQueued(queued *queuedInput) {
	f.lastAttachable = queued.row
	f.currentPrompt = ""
}

func (f *fileState) addQueueOperationMetadata(queued *queuedInput, operation string, at *time.Time, ordinal int) {
	entry := map[string]any{"operation": operation, "sourceOrdinal": ordinal}
	if at != nil {
		entry["timestamp"] = at.UTC().Format(time.RFC3339Nano)
	}
	queued.operations = append(queued.operations, entry)
}

// flushQueueOperations writes each queued input's operations into its metadata
// once, rather than re-encoding the growing list on every operation.
func (f *fileState) flushQueueOperations() {
	for _, queued := range f.queuedByIdentity {
		if len(queued.operations) == 0 {
			continue
		}
		metadata := decodeMetadata(queued.row.Metadata)
		metadata["queueOperations"] = queued.operations
		queued.row.Metadata = encodeMetadata(metadata)
	}
}

func (f *fileState) addQueuedAttachmentMetadata(queued *queuedInput, attachment claudetranscript.Attachment) {
	metadata := decodeMetadata(queued.row.Metadata)
	if attachment.CommandMode != "" {
		metadata["commandMode"] = attachment.CommandMode
	}
	if len(attachment.Origin) > 0 {
		metadata["origin"] = json.RawMessage(attachment.Origin)
	}
	queued.row.Metadata = encodeMetadata(metadata)
}

func (f *fileState) mergeQueuedMetadata(queued *queuedInput, addition json.RawMessage) {
	metadata := decodeMetadata(queued.row.Metadata)
	for key, value := range decodeMetadata(addition) {
		metadata[key] = value
	}
	queued.row.Metadata = encodeMetadata(metadata)
}

func decodeMetadata(raw json.RawMessage) map[string]any {
	metadata := map[string]any{}
	_ = json.Unmarshal(raw, &metadata)
	return metadata
}

func encodeMetadata(metadata map[string]any) json.RawMessage {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return json.RawMessage(`{"queued":true}`)
	}
	return raw
}

func queueIdentity(at *time.Time, ordinal int, text string) string {
	if at != nil {
		return at.UTC().Format(time.RFC3339Nano) + "\x00" + text
	}
	return fmt.Sprintf("line:%d\x00%s", ordinal, text)
}
