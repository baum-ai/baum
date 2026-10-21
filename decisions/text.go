package decisions

import (
	"fmt"
	"strings"
)

// Text is the transcript as the extractor reads it (ARCHITECTURE.md,
// "decisions"). The output is deterministic for a given transcript.
func (t *Transcript) Text() string {
	var b strings.Builder
	for i, turn := range t.Turns {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## Turn %d\n", turn.Ordinal)
		if turn.Human != "" {
			field(&b, "", "Human", turn.Human)
		} else {
			b.WriteString("Human: (no prompt; started by the system or the agent)\n")
		}
		for _, event := range turn.Events {
			writeEvent(&b, event)
		}
		if len(turn.EditedFiles) > 0 {
			fmt.Fprintf(&b, "Edited files: %s\n", strings.Join(turn.EditedFiles, ", "))
		}
	}
	return b.String()
}

func writeEvent(b *strings.Builder, event Event) {
	switch event.Kind {
	case EventAgentText:
		field(b, "", "Agent", event.Text)
	case EventQuestion:
		b.WriteString("Agent asked:\n")
		for _, question := range event.Questions {
			field(b, "  ", "- Question", question.Text)
			if len(question.Options) > 0 {
				field(b, "    ", "Options", strings.Join(question.Options, " | "))
			}
			switch {
			case question.Answer == "" && event.Discuss:
				b.WriteString("    Answer: (none; the human chose to discuss the questions first)\n")
			case question.Answer == "":
				b.WriteString("    Answer: (none recorded)\n")
			case question.Picked:
				field(b, "    ", "Answer (offered option)", question.Answer)
			default:
				field(b, "    ", "Answer (free text)", question.Answer)
			}
			if question.Note != "" {
				field(b, "    ", "Human note", question.Note)
			}
		}
		if event.Feedback != "" {
			field(b, "  ", "Human declined to answer and said", event.Feedback)
		}
	case EventPlan:
		field(b, "", "Agent proposed plan ("+planLabel(event.Outcome)+")", event.Text)
		if event.Feedback != "" {
			field(b, "  ", "Human feedback", event.Feedback)
		}
	case EventDenial:
		if event.Feedback != "" {
			field(b, "", "Human rejected the "+event.Tool+" call and said", event.Feedback)
		} else {
			fmt.Fprintf(b, "Human rejected the %s call.\n", event.Tool)
		}
	case EventSubagent:
		field(b, "", "Subagent report", event.Text)
	}
}

func planLabel(outcome PlanOutcome) string {
	switch outcome {
	case PlanApproved:
		return "human approved"
	case PlanRejected:
		return "human rejected"
	}
	return "no recorded response"
}

// field writes "label: text" with continuation lines indented under it.
func field(b *strings.Builder, indent, label, text string) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	fmt.Fprintf(b, "%s%s: %s\n", indent, label, strings.TrimRight(lines[0], " \t"))
	for _, line := range lines[1:] {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(b, "%s  %s\n", indent, line)
	}
}
