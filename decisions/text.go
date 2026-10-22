package decisions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// Text is the transcript as the extractor reads it (ARCHITECTURE.md,
// "decisions"). The output is deterministic for a given transcript.
func (t *Transcript) Text() string {
	return t.text(0)
}

// text is Text with, for a positive cutoff, the extension marker line after
// the cutoff turn's block (ExtractOptions.Cutoff).
func (t *Transcript) text(cutoff int) string {
	var b strings.Builder
	marked := cutoff <= 0
	for i, turn := range t.Turns {
		if i > 0 {
			b.WriteString("\n")
		}
		if !marked && turn.Ordinal > cutoff {
			writeCutoff(&b, cutoff)
			marked = true
		}
		writeTurn(&b, turn)
	}
	if !marked {
		b.WriteString("\n")
		writeCutoff(&b, cutoff)
	}
	return b.String()
}

func writeCutoff(b *strings.Builder, cutoff int) {
	fmt.Fprintf(b, "--- Decisions through turn %d are already extracted; decisions after this line may already be listed ---\n\n", cutoff)
}

func writeTurn(b *strings.Builder, turn Turn) {
	fmt.Fprintf(b, "## Turn %d\n", turn.Ordinal)
	if turn.Human != "" {
		field(b, "", "Human", turn.Human)
	} else {
		b.WriteString("Human: (no prompt; started by the system or the agent)\n")
	}
	for _, event := range turn.Events {
		writeEvent(b, event)
	}
	if len(turn.EditedFiles) > 0 {
		fmt.Fprintf(b, "Edited files: %s\n", strings.Join(turn.EditedFiles, ", "))
	}
}

// ContentHash is the SHA-256, in hex, of t's Text: the content a cached
// extraction of t is keyed on.
func ContentHash(t *Transcript) string {
	sum := sha256.Sum256([]byte(t.Text()))
	return hex.EncodeToString(sum[:])
}

// PrefixHash is the ContentHash of t without its last turn, which a later
// extraction of the grown session extends from (ExtensionCutoff); "" when t
// has at most one turn.
func PrefixHash(t *Transcript) string {
	if len(t.Turns) <= 1 {
		return ""
	}
	return ContentHash(&Transcript{Turns: t.Turns[:len(t.Turns)-1]})
}

// ExtensionCutoff finds the prefix of t's turns, short of all of them, whose
// text hashes to prefixHash, an earlier extraction's PrefixHash. It returns
// the ordinal of that prefix's last turn, the cutoff to extend that
// extraction with, and whether one matched.
func ExtensionCutoff(t *Transcript, prefixHash string) (int, bool) {
	if prefixHash == "" || len(t.Turns) < 2 {
		return 0, false
	}
	// Sum leaves the hash running, so each prefix is hashed once.
	hash := sha256.New()
	var b strings.Builder
	for i, turn := range t.Turns[:len(t.Turns)-1] {
		b.Reset()
		if i > 0 {
			b.WriteString("\n")
		}
		writeTurn(&b, turn)
		io.WriteString(hash, b.String())
		if hex.EncodeToString(hash.Sum(nil)) == prefixHash {
			return turn.Ordinal, true
		}
	}
	return 0, false
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
