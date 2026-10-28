package claudesession

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/session"
)

const hostedReportSession = "11111111-2222-4333-8444-555555555555"

func hostedReportLine(fields map[string]any) string {
	line := map[string]any{"sessionId": hostedReportSession, "cwd": "/repo", "isSidechain": false}
	for key, value := range fields {
		line[key] = value
	}
	raw, err := json.Marshal(line)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func hostedAt(second int) string { return fmt.Sprintf("2026-10-01T10:00:%02dZ", second) }

func hostedCallLine(second int, uuid, messageID, toolUseID, name string, input map[string]any) string {
	return hostedReportLine(map[string]any{
		"type": "assistant", "uuid": uuid, "timestamp": hostedAt(second),
		"message": map[string]any{"id": messageID, "role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": toolUseID, "name": name, "input": input},
		}},
	})
}

func hostedResultLine(second int, uuid, toolUseID, text string, toolUseResult map[string]any) string {
	return hostedReportLine(map[string]any{
		"type": "user", "uuid": uuid, "timestamp": hostedAt(second), "toolUseResult": toolUseResult,
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": toolUseID, "content": text},
		}},
	})
}

func hostedHandbackEnqueue(second int, agentID, report string) string {
	indented := "  " + strings.ReplaceAll(report, "\n", "\n  ")
	return hostedReportLine(map[string]any{
		"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(second),
		"content": fmt.Sprintf("<agent-message from=%q>\n[Subagent hand-back] The report follows:\n%s\n</agent-message>", agentID, indented),
	})
}

func hostedOutputOf(t *testing.T, graph *session.Graph, toolUseID string) (string, string) {
	t.Helper()
	for _, row := range graph.VendorIDs {
		if row.OurType != session.EntityToolUse || row.VendorIDKind != "tool_use.id" || row.VendorID != toolUseID {
			continue
		}
		var use session.ToolUse
		for _, candidate := range graph.ToolUses {
			if candidate.ID == row.OurID {
				use = candidate
			}
		}
		var bag map[string]any
		_ = json.Unmarshal(use.Metadata, &bag)
		detail, _ := bag[session.SubagentDetailKey].(string)
		for _, result := range graph.ToolResults {
			if result.ToolUseID == use.ID && result.OutputText != nil {
				return *result.OutputText, detail
			}
		}
		return "", detail
	}
	t.Fatalf("no tool use %s", toolUseID)
	return "", ""
}

// A resumed agent's next report answers the SendMessage that resumed it, not
// the launch, which keeps the first one. A hand-back the queue carried alone
// still reaches its call, and never becomes a human prompt.
func TestHostedHandbacksAnswerTheCallsWaitingInOrder(t *testing.T) {
	graph, report := hostedBuildClaudeLines(t,
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-1", "timestamp": hostedAt(1), "promptId": "p-1",
			"message": map[string]any{"role": "user", "content": "Review it"}}),
		hostedCallLine(2, "a-1", "m-1", "toolu_launch", "Agent", map[string]any{"description": "Review", "run_in_background": true}),
		hostedResultLine(3, "u-2", "toolu_launch", "Async agent launched successfully.",
			map[string]any{"status": "async_launched", "isAsync": true, "agentId": "aaaa"}),
		hostedHandbackEnqueue(4, "aaaa", "First pass: no defects."),
		hostedCallLine(5, "a-2", "m-2", "toolu_resume", "SendMessage", map[string]any{"to": "aaaa", "message": "Check the tests too"}),
		hostedResultLine(6, "u-3", "toolu_resume", `{"success":true,"message":"Resuming agent aaaa"}`,
			map[string]any{"success": true, "resumedAgentId": "aaaa"}),
		hostedHandbackEnqueue(7, "aaaa", "Second pass: the tests cover\nempty input."),
		hostedHandbackEnqueue(8, "bbbb", "From an agent this transcript never launched."),
	)
	if !report.Lines.Balanced() {
		t.Fatalf("lines = %+v", report.Lines)
	}
	if output, detail := hostedOutputOf(t, graph, "toolu_launch"); output != "First pass: no defects." || detail != string(session.SubagentReport) {
		t.Fatalf("launch output %q detail %q", output, detail)
	}
	if output, detail := hostedOutputOf(t, graph, "toolu_resume"); output != "Second pass: the tests cover\nempty input." || detail != string(session.SubagentReport) {
		t.Fatalf("resume output %q detail %q", output, detail)
	}
	handbacks := 0
	for _, input := range graph.UserInputs {
		if strings.Contains(input.Text, "agent-message") {
			handbacks++
			if input.Kind != session.KindOther || input.Origin != session.OriginSystem {
				t.Fatalf("a hand-back became a %s input from %s: %+v", input.Kind, input.Origin, input)
			}
		}
	}
	if handbacks != 3 {
		t.Fatalf("hand-back inputs = %d, want one per enqueue", handbacks)
	}
}

// A hand-back enqueue keeps the input it always made, so the turn ordinals
// stored decisions, review bundles and links cite do not move. It is a system
// input, not a human prompt.
func TestHostedHandbackEnqueueKeepsItsTurnOrdinal(t *testing.T) {
	graph, _ := hostedBuildClaudeLines(t,
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-1", "timestamp": hostedAt(1), "promptId": "p-1", "promptSource": "typed",
			"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": "Review it"}}),
		hostedCallLine(2, "a-1", "m-1", "toolu_launch", "Agent", map[string]any{"description": "Review", "run_in_background": true}),
		hostedResultLine(3, "u-2", "toolu_launch", "Async agent launched successfully.",
			map[string]any{"status": "async_launched", "isAsync": true, "agentId": "aaaa"}),
		hostedHandbackEnqueue(4, "aaaa", "No defects."),
		hostedHandbackDelivered(5, "u-h", "aaaa", "No defects."),
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-3", "timestamp": hostedAt(6), "promptId": "p-2", "promptSource": "typed",
			"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": "Now fix it"}}),
	)
	type row struct {
		ordinal int
		kind    session.InputKind
		origin  session.InputOrigin
		text    string
	}
	var got []row
	for _, input := range graph.UserInputs {
		text := input.Text
		if len(text) > 30 {
			text = text[:30]
		}
		got = append(got, row{input.Ordinal, input.Kind, input.Origin, text})
	}
	want := []row{
		{1, session.KindPrompt, session.OriginHuman, "Review it"},
		{2, session.KindOther, session.OriginSystem, `<agent-message from="aaaa">` + "\n[S"},
		{3, session.KindOther, session.OriginSystem, "Another Claude session sent a "},
		{4, session.KindPrompt, session.OriginHuman, "Now fix it"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("inputs =\n%v\nwant\n%v", got, want)
	}
	if output, _ := hostedOutputOf(t, graph, "toolu_launch"); output != "No defects." {
		t.Fatalf("launch output %q", output)
	}
}

// Reports arrive in order. Once a later call is answered, an earlier one left
// unanswered is not waiting any more: the next report goes after the answer.
func TestHostedAReportNeverGoesBeforeTheLatestAnswer(t *testing.T) {
	graph, _ := hostedBuildClaudeLines(t,
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-1", "timestamp": hostedAt(1), "promptId": "p-1",
			"message": map[string]any{"role": "user", "content": "Review it"}}),
		hostedCallLine(2, "a-1", "m-1", "toolu_launch", "Agent", map[string]any{"description": "Review", "run_in_background": true}),
		hostedResultLine(3, "u-2", "toolu_launch", "Async agent launched successfully.",
			map[string]any{"status": "async_launched", "isAsync": true, "agentId": "aaaa"}),
		hostedCallLine(4, "a-2", "m-2", "toolu_again", "Agent", map[string]any{"description": "Review again"}),
		hostedResultLine(5, "u-3", "toolu_again", "Synchronous report.",
			map[string]any{"status": "completed", "agentId": "aaaa"}),
		hostedHandbackEnqueue(6, "aaaa", "Late report."),
	)
	if output, detail := hostedOutputOf(t, graph, "toolu_launch"); output != "Async agent launched successfully." || detail != string(session.SubagentNone) {
		t.Fatalf("earlier launch output %q detail %q, want it left unanswered", output, detail)
	}
	if output, _ := hostedOutputOf(t, graph, "toolu_again"); output != "Synchronous report." {
		t.Fatalf("later call output %q", output)
	}
}

// A notification's <result> is the report when it is not a pointer to the
// hand-back, and a later hand-back of the same agent replaces it.
func TestHostedNotificationResultIsAReportUntilAHandbackArrives(t *testing.T) {
	notification := func(second int, result string) string {
		return hostedReportLine(map[string]any{
			"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(second),
			"content": "<task-notification>\n<task-id>aaaa</task-id>\n<tool-use-id>toolu_launch</tool-use-id>\n<status>completed</status>\n<summary>Agent \"Review\" finished</summary>\n<result>" + result + "</result>\n</task-notification>",
		})
	}
	lines := []string{
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-1", "timestamp": hostedAt(1), "promptId": "p-1",
			"message": map[string]any{"role": "user", "content": "Review it"}}),
		hostedCallLine(2, "a-1", "m-1", "toolu_launch", "Agent", map[string]any{"description": "Review", "run_in_background": true}),
		hostedResultLine(3, "u-2", "toolu_launch", "Async agent launched successfully.",
			map[string]any{"status": "async_launched", "isAsync": true, "agentId": "aaaa"}),
	}
	pointer, _ := hostedBuildClaudeLines(t, append(lines, notification(4, `This agent's report was delivered to you as a message from "aaaa" (its SubagentHandback call).`))...)
	if output, detail := hostedOutputOf(t, pointer, "toolu_launch"); output != `Agent "Review" finished` || detail != string(session.SubagentNone) {
		t.Fatalf("pointer notification: output %q detail %q", output, detail)
	}
	inline, _ := hostedBuildClaudeLines(t, append(lines, notification(4, "Inline report."))...)
	if output, detail := hostedOutputOf(t, inline, "toolu_launch"); output != "Inline report." || detail != string(session.SubagentReport) {
		t.Fatalf("inline notification: output %q detail %q", output, detail)
	}
	replaced, _ := hostedBuildClaudeLines(t, append(lines, notification(4, "Inline report."), hostedHandbackEnqueue(5, "aaaa", "Hand-back report."))...)
	if output, _ := hostedOutputOf(t, replaced, "toolu_launch"); output != "Hand-back report." {
		t.Fatalf("hand-back after notification: output %q", output)
	}
}

func hostedHandbackDelivered(second int, uuid, agentID, report string) string {
	body := "[Subagent hand-back] The report follows:\n  " + strings.ReplaceAll(report, "\n", "\n  ")
	return hostedReportLine(map[string]any{
		"type": "user", "uuid": uuid, "timestamp": hostedAt(second), "isMeta": true, "promptSource": "system",
		"origin":  map[string]any{"kind": "peer", "from": agentID, "handback": true, "body": body},
		"message": map[string]any{"role": "user", "content": "Another Claude session sent a message:\n<agent-message from=\"" + agentID + "\">\n" + body + "\n</agent-message>"},
	})
}

func hostedLaunchAndResume(resumeAfter ...string) []string {
	lines := []string{
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-1", "timestamp": hostedAt(1), "promptId": "p-1",
			"message": map[string]any{"role": "user", "content": "Review it"}}),
		hostedCallLine(2, "a-1", "m-1", "toolu_launch", "Agent", map[string]any{"description": "Review", "run_in_background": true}),
		hostedResultLine(3, "u-2", "toolu_launch", "Async agent launched successfully.",
			map[string]any{"status": "async_launched", "isAsync": true, "agentId": "aaaa"}),
	}
	lines = append(lines, resumeAfter...)
	return append(lines,
		hostedCallLine(10, "a-2", "m-2", "toolu_resume", "SendMessage", map[string]any{"to": "aaaa", "message": "Again"}),
		hostedResultLine(11, "u-3", "toolu_resume", `{"success":true}`, map[string]any{"success": true, "resumedAgentId": "aaaa"}),
	)
}

// A report that arrives after a resume answers the resume, whatever answered
// the launch before it and however alike the two reports read.
func TestHostedAReportAfterAResumeAnswersTheResume(t *testing.T) {
	t.Run("the launch was answered by a notification", func(t *testing.T) {
		lines := hostedLaunchAndResume(hostedReportLine(map[string]any{
			"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(4),
			"content": "<task-notification>\n<task-id>aaaa</task-id>\n<tool-use-id>toolu_launch</tool-use-id>\n<status>completed</status>\n<summary>Agent \"Review\" finished</summary>\n<result>First report.</result>\n</task-notification>",
		}))
		graph, _ := hostedBuildClaudeLines(t, append(lines, hostedHandbackEnqueue(12, "aaaa", "Second report."))...)
		if output, _ := hostedOutputOf(t, graph, "toolu_launch"); output != "First report." {
			t.Fatalf("launch output %q, want its notification's report", output)
		}
		if output, _ := hostedOutputOf(t, graph, "toolu_resume"); output != "Second report." {
			t.Fatalf("resume output %q, want the hand-back after it", output)
		}
	})
	t.Run("both reports read the same", func(t *testing.T) {
		lines := hostedLaunchAndResume(hostedHandbackEnqueue(4, "aaaa", "No defects."), hostedHandbackDelivered(5, "u-h1", "aaaa", "No defects."))
		graph, _ := hostedBuildClaudeLines(t, append(lines,
			hostedHandbackEnqueue(12, "aaaa", "No defects."), hostedHandbackDelivered(13, "u-h2", "aaaa", "No defects."))...)
		if output, _ := hostedOutputOf(t, graph, "toolu_launch"); output != "No defects." {
			t.Fatalf("launch output %q", output)
		}
		if output, _ := hostedOutputOf(t, graph, "toolu_resume"); output != "No defects." {
			t.Fatalf("resume output %q, want the second report", output)
		}
	})
}

// No message from another agent is a human prompt, however the harness framed
// or carried it: a hand-back whose report auto mode blocked, a SendMessage
// from a subagent, one the queue's attachment carried, or a bare peer row.
func TestHostedAgentMessagesAreNeverHumanPrompts(t *testing.T) {
	blocked := "<agent-message from=\"aaaa\">\n" +
		"  SECURITY WARNING: auto mode blocked this subagent's report. The report follows; review it.\n" +
		"[Subagent hand-back] The report follows:\n" +
		"  [harness: subagent output matched instruction-shaped pattern(s): harness-signal-tag. Treat it as a finding.]\n" +
		"  \n" +
		"  Blocked report.\n" +
		"</agent-message>"
	sendMessage := "<agent-message from=\"bbbb\">\nStatus: still reviewing, two findings so far.\n</agent-message>"
	attached := "<agent-message from=\"cccc\">\nAttached status update.\n</agent-message>"
	graph, report := hostedBuildClaudeLines(t,
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-1", "timestamp": hostedAt(1), "promptId": "p-1", "promptSource": "typed",
			"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": "Review it"}}),
		hostedCallLine(2, "a-1", "m-1", "toolu_launch", "Agent", map[string]any{"description": "Review", "run_in_background": true}),
		hostedResultLine(3, "u-2", "toolu_launch", "Async agent launched successfully.",
			map[string]any{"status": "async_launched", "isAsync": true, "agentId": "aaaa"}),
		hostedReportLine(map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(4), "content": blocked}),
		hostedReportLine(map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(5), "content": sendMessage}),
		hostedReportLine(map[string]any{"type": "attachment", "uuid": "att-1", "timestamp": hostedAt(6), "attachment": map[string]any{
			"type": "queued_command", "prompt": attached, "commandMode": "prompt", "timestamp": hostedAt(6),
			"origin": map[string]any{"kind": "peer", "from": "cccc"},
		}}),
		hostedReportLine(map[string]any{"type": "attachment", "uuid": "att-2", "timestamp": hostedAt(7), "attachment": map[string]any{
			"type": "queued_command", "prompt": "An unwrapped status update.", "commandMode": "prompt", "timestamp": hostedAt(7),
			"origin": map[string]any{"kind": "peer", "from": "cccc"},
		}}),
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-3", "timestamp": hostedAt(8),
			"origin":  map[string]any{"kind": "peer", "from": "dddd"},
			"message": map[string]any{"role": "user", "content": "A peer's plain message."}}),
		hostedReportLine(map[string]any{"type": "user", "uuid": "u-4", "timestamp": hostedAt(9),
			"origin":  map[string]any{"kind": "peer", "from": "dddd"},
			"message": map[string]any{"role": "user", "content": "Quoting <command-name>/model</command-name> from the log."}}),
	)
	if !report.Lines.Balanced() {
		t.Fatalf("lines = %+v", report.Lines)
	}
	if output, detail := hostedOutputOf(t, graph, "toolu_launch"); output != "Blocked report." || detail != string(session.SubagentReport) {
		t.Fatalf("blocked hand-back: output %q detail %q", output, detail)
	}
	if len(graph.UserInputs) != 7 {
		t.Fatalf("inputs = %d, want the prompt and one per agent message", len(graph.UserInputs))
	}
	for _, input := range graph.UserInputs[1:] {
		if input.Kind != session.KindOther || input.Origin != session.OriginSystem {
			t.Fatalf("an agent's message became a %s input from %s: %q", input.Kind, input.Origin, input.Text)
		}
	}
}

// A queued agent message and the `user` row that later delivers the same
// text are one input, as a queued prompt and its row are.
// A row only a peer origin marks as another agent's, behind a queue that
// could not tell, makes that input a system one too.
func TestHostedAQueuedAgentMessageKeepsOneInput(t *testing.T) {
	for name, tc := range map[string]struct {
		text   string
		origin map[string]any
	}{
		"wrapped":            {"<agent-message from=\"bbbb\">\nStatus: done.\n</agent-message>", nil},
		"peer origin, plain": {"Status: done.", map[string]any{"kind": "peer", "from": "bbbb"}},
		// Quoting a slash command does not make it the person's command.
		"peer origin, quoting a slash command": {
			"I ran <command-name>/review</command-name> as asked.", map[string]any{"kind": "peer", "from": "bbbb"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			row := map[string]any{"type": "user", "uuid": "u-2", "timestamp": hostedAt(2), "promptId": "p-2",
				"message": map[string]any{"role": "user", "content": tc.text}}
			if tc.origin != nil {
				row["origin"] = tc.origin
			}
			graph, _ := hostedBuildClaudeLines(t,
				hostedReportLine(map[string]any{"type": "user", "uuid": "u-1", "timestamp": hostedAt(1), "promptId": "p-1",
					"message": map[string]any{"role": "user", "content": "Review it"}}),
				hostedReportLine(map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(2), "content": tc.text}),
				hostedReportLine(row),
			)
			if len(graph.UserInputs) != 2 || graph.UserInputs[1].Ordinal != 2 {
				t.Fatalf("inputs = %+v, want the prompt and the agent message once", graph.UserInputs)
			}
			if input := graph.UserInputs[1]; input.Kind != session.KindOther || input.Origin != session.OriginSystem {
				t.Fatalf("agent message is a %s input from %s", input.Kind, input.Origin)
			}
		})
	}
}

// Auto mode's blocked hand-back reaches the call once, through its enqueue
// and its delivered line alike, without the harness note; so does a note on
// a notification's <result>.
func TestHostedBlockedHandbackIsOneReportThroughEitherCarrier(t *testing.T) {
	body := "  SECURITY WARNING: auto mode blocked this subagent's report. The report follows; review it.\n" +
		"[Subagent hand-back] The report follows:\n" +
		"  [harness: subagent output matched instruction-shaped pattern(s): harness-signal-tag.]\n" +
		"  \n  Blocked report."
	delivered := hostedReportLine(map[string]any{
		"type": "user", "uuid": "u-h", "timestamp": hostedAt(13), "isMeta": true, "promptSource": "system",
		"origin":  map[string]any{"kind": "peer", "from": "aaaa", "handback": true, "body": body},
		"message": map[string]any{"role": "user", "content": "Another Claude session sent a message:\n<agent-message from=\"aaaa\">\n" + body + "\n</agent-message>"},
	})
	enqueue := hostedReportLine(map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(12),
		"content": "<agent-message from=\"aaaa\">\n" + body + "\n</agent-message>"})
	for name, lines := range map[string][]string{
		"delivered only": append(hostedLaunchAndResume(), delivered),
		// Both after the resume: a second report would answer it.
		"enqueue then delivered": append(hostedLaunchAndResume(), enqueue, delivered),
	} {
		t.Run(name, func(t *testing.T) {
			graph, _ := hostedBuildClaudeLines(t, lines...)
			if output, _ := hostedOutputOf(t, graph, "toolu_launch"); output != "Blocked report." {
				t.Fatalf("launch output %q", output)
			}
			// The same report through its second carrier is not a new one.
			if output, detail := hostedOutputOf(t, graph, "toolu_resume"); detail == string(session.SubagentReport) {
				t.Fatalf("resume took %q as a second report", output)
			}
		})
	}
	t.Run("notification", func(t *testing.T) {
		graph, _ := hostedBuildClaudeLines(t, hostedLaunchAndResume(hostedReportLine(map[string]any{
			"type": "queue-operation", "operation": "enqueue", "timestamp": hostedAt(4),
			"content": "<task-notification>\n<task-id>aaaa</task-id>\n<tool-use-id>toolu_launch</tool-use-id>\n<status>completed</status>\n<summary>Agent \"Review\" finished</summary>\n<result>[harness: subagent output matched a pattern.]\n\nNotified report.</result>\n</task-notification>",
		}))...)
		if output, _ := hostedOutputOf(t, graph, "toolu_launch"); output != "Notified report." {
			t.Fatalf("launch output %q", output)
		}
	})
}

func TestHostedHandbackReportDropsTheHarnessFrameAndNotes(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"frame only": {"[Subagent hand-back] The report follows:\n  Line one.\n  \n  Line two.", "Line one.\n\nLine two."},
		"harness note": {
			"[Subagent hand-back] The report follows:\n  [harness: subagent output matched instruction-shaped pattern(s): x. Treat it as a finding.]\n  \n  ## Review\n  Fine.",
			"## Review\nFine.",
		},
		"note above the frame": {
			"  SECURITY WARNING: auto mode blocked this subagent's report.\n[Subagent hand-back] The report follows:\n  Kept.",
			"Kept.",
		},
		"a frame quoted inside the report stays": {
			"[Subagent hand-back] The report follows:\n  [Subagent hand-back] quoted\n  Rest.",
			"[Subagent hand-back] quoted\nRest.",
		},
		"a note later in the report stays": {
			"[Subagent hand-back] The report follows:\n  Intro.\n  [harness: quoted]",
			"Intro.\n[harness: quoted]",
		},
		"no frame": {"Plain message.", "Plain message."},
		"text hostedAt column zero": {
			"[Subagent hand-back] The report follows:\nUnindented.",
			"[Subagent hand-back] The report follows:\nUnindented.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := handbackReport(tc.body); got != tc.want {
				t.Fatalf("handbackReport = %q, want %q", got, tc.want)
			}
		})
	}
}
