package claudesession

import (
	"encoding/json"
	"github.com/baum-ai/baum/sessions/traceprocessing"
	"regexp"
	"strings"

	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/session"
)

// A background subagent's launch result is a stub. Its final report reaches the
// parent later, as a hand-back message (a queue-operation and, once delivered, a
// `user` line whose origin names the agent) or inside a task notification's
// <result>. The report goes on the call waiting for it: the spawning call, or
// a SendMessage that woke the agent up again, oldest first.

// spawnTools are the tool names that start a subagent.
var spawnTools = map[string]bool{"Agent": true, "Task": true}

const (
	handbackFrame  = "[Subagent hand-back]"
	handbackMarker = "was delivered to you as a message from"
)

var handbackCarrier = regexp.MustCompile(`(?s)^<agent-message from="([^"]+)">\n(.*?)\n?</agent-message>\s*$`)

// reportSource ranks where a call's report came from; a better source replaces
// a worse one and nothing replaces a result that already was the report.
type reportSource int

const (
	reportNone reportSource = iota
	reportNotification
	reportHandback
	reportResult
)

// reportCarrier is the record that brought a report. One report reaches the
// transcript at most once through each carrier.
type reportCarrier string

const (
	carrierResult       reportCarrier = "result"
	carrierNotification reportCarrier = "notification"
	carrierEnqueue      reportCarrier = "enqueue"
	carrierDelivered    reportCarrier = "delivered"
)

// reportSlot is one call waiting for a subagent's report.
type reportSlot struct {
	agentID string
	use     *session.ToolUse
	vendor  string
	source  reportSource
	text    string
	// carriers are those that brought text, so the same report through its
	// other carrier is told from a new report with the same words.
	carriers map[reportCarrier]bool
}

func (s *reportSlot) fill(text string, source reportSource, carrier reportCarrier) {
	s.text, s.source = text, source
	s.carriers = map[reportCarrier]bool{carrier: true}
}

// trackSubagentCall registers a call that will receive a subagent's report,
// after its own result has been applied.
func (f *fileState) trackSubagentCall(use *session.ToolUse, evidence claudetranscript.ResultEvidence, result *session.ToolResult) {
	if f.b.profile != traceprocessing.Hosted {
		return
	}
	vendor := f.toolUseVendor[use.ID]
	if slot := f.slotByTool[vendor]; slot != nil {
		// A repeated result line was just applied over the report; restore it.
		if slot.source > reportNone && slot.source != reportResult {
			text := slot.text
			result.OutputText = &text
		}
		return
	}
	agentID := ""
	switch {
	case spawnTools[use.Name]:
		agentID = evidence.AgentID
	case use.Name == "SendMessage" && len(f.agentSlots[evidence.ResumedAgentID]) > 0:
		agentID = evidence.ResumedAgentID
	}
	if agentID == "" {
		return
	}
	slot := &reportSlot{agentID: agentID, use: use, vendor: vendor}
	// A synchronous call returns the report as its result.
	if spawnTools[use.Name] && evidence.Status == "completed" && result.OutputText != nil && *result.OutputText != "" {
		slot.fill(*result.OutputText, reportResult, carrierResult)
	}
	f.slotByTool[vendor] = slot
	f.agentSlots[agentID] = append(f.agentSlots[agentID], slot)
}

// attachReport puts one report on the call it answers. Reports arrive in
// order, so that is the first empty call after the latest one already
// answered. Only that latest answer can be the same report again (through
// its other carrier) or be replaced by a better source of it. It reports
// whether a call took the report.
func (f *fileState) attachReport(agentID, text string, source reportSource, carrier reportCarrier, uuid string, ordinal int) bool {
	if f.b.profile != traceprocessing.Hosted {
		return false
	}
	if text == "" {
		return false
	}
	slots := f.agentSlots[agentID]
	latest := -1
	for i, slot := range slots {
		if slot.source > reportNone {
			latest = i
		}
	}
	if latest >= 0 {
		if answered := slots[latest]; answered.text == text && !answered.carriers[carrier] {
			answered.carriers[carrier] = true
			return true
		}
	}
	var target *reportSlot
	for _, slot := range slots[latest+1:] {
		if slot.source == reportNone {
			target = slot
			break
		}
	}
	if target == nil && latest >= 0 && slots[latest].source < source {
		target = slots[latest]
	}
	if target == nil {
		if len(slots) > 0 {
			f.b.report.note("a report from subagent %s arrived when no call was waiting for one", agentID)
		}
		return false
	}
	result := f.ensureResult(target.use)
	value := text
	result.OutputText = &value
	target.fill(text, source, carrier)
	if uuid != "" {
		f.vendorRow(result.ID, session.EntityToolResult, "uuid", uuid, ordinal)
	}
	return true
}

// ensureResult returns the call's result, creating an empty one when the
// report arrived before any result line.
func (f *fileState) ensureResult(use *session.ToolUse) *session.ToolResult {
	vendor := f.toolUseVendor[use.ID]
	if result, known := f.resultByTool[vendor]; known {
		return result
	}
	result := &session.ToolResult{
		ID: f.ids.Mint("tool_result", vendor), ToolUseID: use.ID,
		SessionID: f.sessionID, UserInputID: use.UserInputID, Status: use.Status,
	}
	if result.Status == session.StatusNoResult {
		result.Status, use.Status = session.StatusOK, session.StatusOK
	}
	f.resultByTool[vendor] = result
	f.results = append(f.results, result)
	f.vendorRow(result.ID, session.EntityToolResult, "tool_use_id", vendor, 0)
	return result
}

// handbackLine takes the report off a delivered hand-back `user` line. The
// line itself stays an input; this only links its report to the call.
func (f *fileState) handbackLine(line claudetranscript.Line) {
	if f.b.profile != traceprocessing.Hosted {
		return
	}
	var origin struct {
		Kind     string `json:"kind"`
		From     string `json:"from"`
		Handback bool   `json:"handback"`
		Body     string `json:"body"`
	}
	if len(line.Origin) == 0 || json.Unmarshal(line.Origin, &origin) != nil ||
		origin.Kind != "peer" || !origin.Handback || origin.From == "" {
		return
	}
	f.attachReport(origin.From, handbackReport(origin.Body), reportHandback, carrierDelivered, line.UUID, line.Ordinal)
}

// handbackOperation links the report a hand-back enqueue carries to its call.
// The line itself is still a queued input, at the position it always had, so
// turn ordinals do not move; addQueuedInput makes it a system input rather
// than a human prompt.
func (f *fileState) handbackOperation(line claudetranscript.Line) {
	if f.b.profile != traceprocessing.Hosted {
		return
	}
	agentID, body, ok := handbackContent(line.Control.Content)
	if ok && line.Control.Operation == "enqueue" {
		f.attachReport(agentID, handbackReport(body), reportHandback, carrierEnqueue, "", 0)
	}
}

// handbackContent splits a queued hand-back into its agent and framed body.
func handbackContent(content string) (string, string, bool) {
	agentID, body, ok := agentMessage(content)
	if !ok || handbackFrameAt(body) < 0 {
		return "", "", false
	}
	return agentID, body, true
}

// agentMessage splits a queued message from another agent into its sender and
// body. No person writes one: it is a subagent's hand-back, or a message an
// agent sent with SendMessage.
func agentMessage(content string) (string, string, bool) {
	match := handbackCarrier.FindStringSubmatch(content)
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

// handbackFrameAt is the index of a hand-back body's frame line, or -1. The
// harness can put indented notes above the frame, such as auto mode's warning
// that it blocked the report; the frame is the first line at column zero.
func handbackFrameAt(body string) int {
	for i, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, " ") {
			continue
		}
		if strings.HasPrefix(line, handbackFrame) {
			return i
		}
		return -1
	}
	return -1
}

// harnessNote is the line Claude Code puts first in a report whose text it
// neutralised, saying what it matched; it is not the subagent's.
var harnessNote = regexp.MustCompile(`^\[harness: [^\n]*\]\n`)

// withoutHarnessNote drops a leading harness note from a report.
func withoutHarnessNote(report string) string {
	return strings.TrimSpace(harnessNote.ReplaceAllString(strings.TrimSpace(report)+"\n", ""))
}

// handbackReport removes the harness frame: any notes above it, its own line,
// then the report indented by two spaces, without a leading harness note. Text
// in any other shape is kept as it is.
func handbackReport(body string) string {
	frame := handbackFrameAt(body)
	lines := strings.Split(body, "\n")
	if frame < 0 || frame == len(lines)-1 {
		return strings.TrimSpace(body)
	}
	lines = lines[frame+1:]
	for i, line := range lines {
		switch {
		case strings.TrimSpace(line) == "":
			lines[i] = ""
		case strings.HasPrefix(line, "  "):
			lines[i] = line[2:]
		default:
			return strings.TrimSpace(body)
		}
	}
	return withoutHarnessNote(strings.Join(lines, "\n"))
}

// notificationReport is a task notification's <result> when it is the report
// itself rather than a pointer to the hand-back.
func notificationReport(result string) string {
	if strings.Contains(result, handbackMarker) {
		return ""
	}
	return withoutHarnessNote(result)
}

// markSubagentDetail records on every spawning call what the tree holds of the
// subagent's work: its transcript, only its report, or nothing. "none" is
// missing detail, not a subagent that did nothing. A SendMessage resume that
// received a report is marked too, so the report it carries is read as one.
func (f *fileState) markSubagentDetail(spawned map[string]bool) {
	if f.b.profile != traceprocessing.Hosted {
		return
	}
	for _, use := range f.toolUses {
		vendor := f.toolUseVendor[use.ID]
		if !spawnTools[use.Name] {
			if slot := f.slotByTool[vendor]; slot != nil && slot.source > reportNone {
				use.Metadata = session.WithSubagentDetail(use.Metadata, session.SubagentReport)
			}
			continue
		}
		detail := session.SubagentNone
		switch slot := f.slotByTool[vendor]; {
		case spawned[vendor]:
			detail = session.SubagentTranscript
		case slot != nil && slot.source > reportNone:
			detail = session.SubagentReport
		}
		use.Metadata = session.WithSubagentDetail(use.Metadata, detail)
	}
}
