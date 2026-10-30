package fix

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxChatBytes caps the conversation a fix is given: the turns' questions,
// answers, citations and suggestions together.
const MaxChatBytes = 32 << 10

// chatCutMarker ends the newest turn when it alone exceeds MaxChatBytes.
const chatCutMarker = "\n[the rest of this turn was cut]"

// ChatTurn is one answered turn of a reviewer's conversation with the agent
// that wrote the review. Question is stored as the reviewer typed it; Answer
// is the stored, already masked answer markdown. Both are masked again when
// the prompt is built.
type ChatTurn struct {
	Seq        int      `json:"seq"`
	Question   string   `json:"question"`
	Answer     string   `json:"answer"`
	Citations  []string `json:"citations,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// ChatContext is the conversation a fix is made from: the turns up to the
// one the fix was confirmed for, oldest first, within MaxChatBytes. Omitted
// counts the earlier turns CapChat left out.
type ChatContext struct {
	Conversation string     `json:"conversation"`
	Omitted      int        `json:"omitted,omitempty"`
	Turns        []ChatTurn `json:"turns"`
}

func (t ChatTurn) size() int {
	n := len(t.Question) + len(t.Answer) + len(t.Suggestion)
	for _, c := range t.Citations {
		n += len(c)
	}
	return n
}

// CapChat keeps the newest turns of turns (oldest first) that fit in
// MaxChatBytes, dropping the oldest first and counting them in Omitted. A
// turn is never cut, except the newest when it alone exceeds the cap: it is
// then truncated, with a marker, to what fits.
func CapChat(conversation string, turns []ChatTurn) ChatContext {
	out := ChatContext{Conversation: conversation}
	total := 0
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if total+t.size() > MaxChatBytes {
			if len(out.Turns) == 0 {
				out.Turns = append(out.Turns, cutTurn(t))
				i--
			}
			out.Omitted = i + 1
			break
		}
		total += t.size()
		out.Turns = append([]ChatTurn{t}, out.Turns...)
	}
	return out
}

// cutTurn truncates a turn that alone exceeds the cap: the question, the
// suggestion and the citations keep a bounded share, the answer takes the
// rest. The marker is added when any of them was cut.
func cutTurn(t ChatTurn) ChatTurn {
	cut := false
	shorten := func(text string, n int) string {
		if len(text) > n {
			cut = true
			return cutText(text, n)
		}
		return text
	}
	t.Question = shorten(t.Question, MaxChatBytes/4)
	t.Suggestion = shorten(t.Suggestion, MaxChatBytes/8)
	var cites []string
	used := 0
	for _, c := range t.Citations {
		if used+len(c) > MaxChatBytes/8 {
			cut = true
			break
		}
		cites, used = append(cites, c), used+len(c)
	}
	t.Citations = cites
	room := MaxChatBytes - len(t.Question) - len(t.Suggestion) - used - len(chatCutMarker)
	t.Answer = shorten(t.Answer, room)
	if cut {
		t.Answer += chatCutMarker
	}
	return t
}

// cutText cuts text to at most n bytes without splitting a character: it
// steps back over continuation bytes only.
func cutText(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// ChatDigest identifies the conversation a fix was confirmed for, with its
// turns: a different conversation, turn or text changes it. It is empty
// without turns.
func ChatDigest(c ChatContext) string {
	if len(c.Turns) == 0 {
		return ""
	}
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// chatTurnText is a turn as the prompt quotes it.
func chatTurnText(t ChatTurn) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question:\n%s\n\nAnswer:\n%s\n", strings.TrimRight(t.Question, "\n"), strings.TrimRight(t.Answer, "\n"))
	if len(t.Citations) > 0 {
		b.WriteString("\nCitations:\n")
		for _, c := range t.Citations {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if t.Suggestion != "" {
		fmt.Fprintf(&b, "\nSuggested change:\n%s\n", strings.TrimRight(t.Suggestion, "\n"))
	}
	return b.String()
}
