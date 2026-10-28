package claudesession

import (
	"fmt"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/session"
)

// TestReconcileAgainstTheTranscripts counts what is in the files independently of the mapping, and compares.
func TestHostedReconcileAgainstTheTranscripts(t *testing.T) {
	root := corpusDir(t)
	paths := mainTranscripts(t, root)

	var onDisk, stored hostedCounts
	for _, path := range paths {
		tree, err := claudetranscript.DecodeLocalFiles(claudecode.SessionFiles(path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		graph, _, err := hostedBuild(tree)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		onDisk = onDisk.add(hostedCountTranscripts(tree))
		stored.userInputs += len(graph.UserInputs)
		stored.invocations += len(graph.ModelInvocations)
		stored.messages += len(graph.AssistantMessages)
		stored.toolUses += len(graph.ToolUses)
		stored.toolResults += len(graph.ToolResults)
	}

	t.Logf("on disk: %+v", onDisk)
	t.Logf("stored:  %+v", stored)

	// Exact, in both directions.
	if onDisk.toolUses != stored.toolUses {
		t.Errorf("%d tool calls in the files, %d stored", onDisk.toolUses, stored.toolUses)
	}
	if want := onDisk.userInputs + onDisk.queuedInputs; want != stored.userInputs {
		t.Errorf("%d ordinary asks + %d queued-only asks in the files, %d stored", onDisk.userInputs, onDisk.queuedInputs, stored.userInputs)
	}
	if onDisk.invocations != stored.invocations {
		t.Errorf("%d replies in the files, %d stored", onDisk.invocations, stored.invocations)
	}
	if onDisk.messages != stored.messages {
		t.Errorf("%d output blocks in the files, %d stored", onDisk.messages, stored.messages)
	}

	// A call keeps its last result, and a background completion can supply one
	// where no result block ever arrived, so results are bounded rather than equal.
	if stored.toolResults > onDisk.toolResults+onDisk.notifications {
		t.Errorf("%d results stored, but the files hold %d result blocks and %d completions",
			stored.toolResults, onDisk.toolResults, onDisk.notifications)
	}
	if stored.toolResults < onDisk.distinctResultTargets {
		t.Errorf("%d results stored for %d answered calls", stored.toolResults, onDisk.distinctResultTargets)
	}
}

type hostedCounts struct {
	userInputs            int
	queuedInputs          int
	invocations           int
	messages              int
	toolUses              int
	toolResults           int
	distinctResultTargets int
	notifications         int
}

func (c hostedCounts) add(other hostedCounts) hostedCounts {
	return hostedCounts{
		userInputs:            c.userInputs + other.userInputs,
		queuedInputs:          c.queuedInputs + other.queuedInputs,
		invocations:           c.invocations + other.invocations,
		messages:              c.messages + other.messages,
		toolUses:              c.toolUses + other.toolUses,
		toolResults:           c.toolResults + other.toolResults,
		distinctResultTargets: c.distinctResultTargets + other.distinctResultTargets,
		notifications:         c.notifications + other.notifications,
	}
}

// countTranscripts counts what a tree holds, reading the lines directly rather
// than asking the mapping.
func hostedCountTranscripts(tree *claudetranscript.Tree) hostedCounts {
	var out hostedCounts
	for _, file := range tree.Files() {
		replies := map[string]bool{}
		answered := map[string]bool{}
		normalPrompts := map[string]int{}
		queueEnqueues := map[string]map[string]bool{}
		queueOther := map[string]map[string]bool{}
		queueAttachments := map[string]map[string]bool{}
		for _, line := range file.Lines {
			switch line.Class {
			case claudetranscript.ClassUser:
				switch line.UserKind {
				case claudetranscript.UserText, claudetranscript.UserOther:
					out.userInputs++
				case claudetranscript.UserMixed:
					out.userInputs++
				}
				text := line.Message.Text()
				if fromAgent(line, text) || hostedClassifyInput(line, text) == session.KindPrompt {
					normalPrompts[text]++
				}
				for _, block := range line.Message.Blocks() {
					if block.Type == "tool_result" {
						out.toolResults++
						answered[block.ToolUseID] = true
					}
				}
			case claudetranscript.ClassAssistant:
				replies[line.Message.ID] = true
				for _, block := range line.Message.Blocks() {
					if block.Type == "tool_use" {
						out.toolUses++
					} else {
						out.messages++
					}
				}
			case claudetranscript.ClassControl:
				if line.Type == "queue-operation" && line.Control.Content != "" {
					if _, ok := parseNotification(line.Control.Content); ok {
						out.notifications++
					} else if line.Control.Operation == "enqueue" {
						hostedAddQueueCarrier(queueEnqueues, line.Control.Content, hostedSourceQueueKey(line.Timestamp, line.Ordinal))
					} else if line.Control.Operation != "dequeue" {
						// `dequeue` is discarded by the mapping
						// (traceprocessing/README.md, "Claude Code"), so it is not a
						// carrier here either.
						hostedAddQueueCarrier(queueOther, line.Control.Content, hostedSourceQueueKey(line.Timestamp, line.Ordinal))
					}
				}
			case claudetranscript.ClassAttachment:
				attachment := line.Attachment
				if attachment.Type == "queued_command" && attachment.Prompt != "" && queuedPromptAttachment(attachment) {
					if _, notification := parseNotification(attachment.Prompt); !notification {
						hostedAt := attachment.Timestamp
						if hostedAt == nil {
							hostedAt = line.Timestamp
						}
						hostedAddQueueCarrier(queueAttachments, attachment.Prompt, hostedSourceQueueKey(hostedAt, line.Ordinal))
					}
				}
			}
		}
		for text := range hostedQueueTexts(queueEnqueues, queueOther, queueAttachments) {
			queued := max(len(queueEnqueues[text]), len(queueAttachments[text]))
			if queued == 0 {
				queued = len(queueOther[text])
			}
			out.queuedInputs += max(queued-normalPrompts[text], 0)
		}
		out.invocations += len(replies)
		out.distinctResultTargets += len(answered)
	}
	return out
}

func hostedAddQueueCarrier(byText map[string]map[string]bool, text, identity string) {
	if byText[text] == nil {
		byText[text] = map[string]bool{}
	}
	byText[text][identity] = true
}

func hostedQueueTexts(groups ...map[string]map[string]bool) map[string]bool {
	texts := map[string]bool{}
	for _, group := range groups {
		for text := range group {
			texts[text] = true
		}
	}
	return texts
}

func hostedSourceQueueKey(hostedAt *time.Time, ordinal int) string {
	if hostedAt != nil {
		return hostedAt.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("line:%d", ordinal)
}
