package prompt

import (
	_ "embed"
	"strings"

	"github.com/baum-ai/baum/agent"
)

// ChatVersion is the version of the chat instructions; change it with the
// text of chat-public-v1.md or the submit lines below.
const ChatVersion = "chat-public-v1"

//go:embed chat-public-v1.md
var chatPublic string

// The way each backend hands its answer back. Claude Code submits through
// its StructuredOutput tool; Codex's final message is the answer.
const (
	chatSubmitClaude = "## Submitting\n\nSubmit the answer by calling the StructuredOutput tool exactly once, with the answer document's fields as the call's arguments. Never print the answer as text."
	chatSubmitCodex  = "## Submitting\n\nYour final message is the answer document and nothing else: one JSON object matching the output schema, with no Markdown fences or commentary around it. Include every key the schema lists."
)

// Chat returns the instructions of one turn of a chat with the agent that
// wrote a review (internal/chat): the reframing from writing the review to
// answering the reviewer's question, and how the backend submits its answer.
// They go in the user prompt of every turn, because a resumed or forked
// session keeps the system prompt it was recorded with. The caller appends
// the question and its focus, delimited as untrusted data.
func Chat(backend string) string {
	submit := chatSubmitClaude
	if backend == agent.BackendCodex {
		submit = chatSubmitCodex
	}
	return strings.TrimRight(chatPublic, "\n") + "\n\n" + submit + "\n"
}
