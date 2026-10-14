package claudesession

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/baumai/baum/sessions/claudetranscript"
	"github.com/baumai/baum/sessions/session"
)

// A long-running tool hands off: its tool result says the command moved to the
// background, and the real outcome arrives later on a queue-operation carrying a
// task notification. T
var (
	notificationWrapper = regexp.MustCompile(`(?s)<task-notification>(.*?)</task-notification>`)
	notificationField   = map[string]*regexp.Regexp{
		"taskID":     regexp.MustCompile(`(?s)<task-id>(.*?)</task-id>`),
		"toolUseID":  regexp.MustCompile(`(?s)<tool-use-id>(.*?)</tool-use-id>`),
		"status":     regexp.MustCompile(`(?s)<status>(.*?)</status>`),
		"summary":    regexp.MustCompile(`(?s)<summary>(.*?)</summary>`),
		"outputFile": regexp.MustCompile(`(?s)<output-file>(.*?)</output-file>`),
	}
)

type notification struct {
	TaskID     string `json:"task-id,omitempty"`
	ToolUseID  string `json:"tool-use-id,omitempty"`
	Status     string `json:"status,omitempty"`
	Summary    string `json:"summary,omitempty"`
	OutputFile string `json:"output-file,omitempty"`
}

func parseNotification(content string) (notification, bool) {
	body := notificationWrapper.FindStringSubmatch(content)
	if body == nil {
		return notification{}, false
	}
	field := func(name string) string {
		if match := notificationField[name].FindStringSubmatch(body[1]); match != nil {
			return strings.TrimSpace(match[1])
		}
		return ""
	}
	return notification{
		TaskID:     field("taskID"),
		ToolUseID:  field("toolUseID"),
		Status:     field("status"),
		Summary:    field("summary"),
		OutputFile: field("outputFile"),
	}, true
}

// queueOperation maps human queue content as an input and folds background
// completion content into the call it belongs to.
func (f *fileState) queueOperation(line claudetranscript.Line) error {
	if line.Control.Content == "" {
		f.b.report.discard("control/queue-operation (" + operationOr(line.Control.Operation) + ")")
		return nil
	}
	parsed, ok := parseNotification(line.Control.Content)
	if !ok {
		if line.Control.Operation == "dequeue" {
			f.b.report.discard("control/queue-operation (dequeue)")
			return nil
		}
		if f.recordQueuedOperation(line) {
			f.b.report.Lines.Mapped++
		} else {
			f.b.report.Lines.Merged++
		}
		return nil
	}
	if parsed.ToolUseID == "" {
		f.b.report.discard("control/queue-operation (no tool call named)")
		f.b.report.note("a task notification names no tool call and was not stored")
		return nil
	}

	use, known := f.byToolUseID[parsed.ToolUseID]
	if !known {
		f.b.report.discard("control/queue-operation (tool call not in this transcript)")
		f.mergeNote(parsed, "names tool call %s, which is in another transcript", parsed.ToolUseID)
		return nil
	}

	if f.merged(parsed) {
		f.b.report.discard("control/queue-operation (already merged)")
		return nil
	}

	f.applyNotification(use, parsed)
	f.b.report.Lines.Merged++
	return nil
}

func operationOr(operation string) string {
	if operation == "" {
		return "no operation"
	}
	return operation
}

func (f *fileState) applyNotification(use *session.ToolUse, parsed notification) {
	status := notificationStatus(parsed.Status)

	result, known := f.resultByTool[f.toolUseVendor[use.ID]]
	if !known {
		result = &session.ToolResult{
			ID:          f.ids.Mint("tool_result", f.toolUseVendor[use.ID]),
			ToolUseID:   use.ID,
			SessionID:   f.sessionID,
			UserInputID: use.UserInputID,
		}
		f.resultByTool[f.toolUseVendor[use.ID]] = result
		f.results = append(f.results, result)
		f.vendorRow(result.ID, session.EntityToolResult, "tool_use_id", f.toolUseVendor[use.ID], 0)
	}

	result.Status = status
	use.Status = status
	f.lastToolStatus = status
	if parsed.Summary != "" {
		summary := parsed.Summary
		result.OutputText = &summary
	}
	result.Output = mergeNotificationOutput(result.Output, parsed)
	use.Metadata = mergeNotificationMetadata(use.Metadata, parsed)
}

// notificationStatus maps a completion's status onto ours.
func notificationStatus(status string) session.ToolStatus {
	switch strings.ToLower(status) {
	case "completed", "success", "succeeded":
		return session.StatusOK
	case "interrupted", "cancelled", "canceled":
		return session.StatusInterrupted
	default:
		return session.StatusError
	}
}

func mergeNotificationOutput(existing json.RawMessage, parsed notification) json.RawMessage {
	bag := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &bag); err != nil {
			bag = map[string]any{"backgroundHandoff": existing}
		}
	}
	bag["taskNotification"] = parsed
	raw, err := json.Marshal(bag)
	if err != nil {
		return existing
	}
	return raw
}

func mergeNotificationMetadata(existing json.RawMessage, parsed notification) json.RawMessage {
	bag := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &bag)
	}
	if parsed.TaskID != "" {
		bag["taskId"] = parsed.TaskID
	}
	if parsed.OutputFile != "" {
		bag["outputFile"] = parsed.OutputFile
	}
	raw, err := json.Marshal(bag)
	if err != nil {
		return existing
	}
	return raw
}

// merged reports whether this completion has already been applied.
func (f *fileState) merged(parsed notification) bool {
	key := parsed.ToolUseID + "\x00" + parsed.TaskID + "\x00" + parsed.Status
	if f.mergedNotifications[key] {
		return true
	}
	f.mergedNotifications[key] = true
	return false
}

func (f *fileState) mergeNote(parsed notification, format string, args ...any) {
	key := parsed.ToolUseID + "\x00" + fmt.Sprintf(format, args...)
	if f.seenNote[key] {
		return
	}
	f.seenNote[key] = true
	f.b.report.note("a task notification "+format, args...)
}
