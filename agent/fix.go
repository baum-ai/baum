package agent

import "context"

// Fixer is a backend that can run an agent with write access to a checkout,
// for a fix a person asked for: the agent edits the files of
// Request.Directory and its final message, a short summary of what it did,
// is Result.Document. Request.Schema is not used. The agent gets no network
// access and no git credentials; the caller commits and pushes what it
// changed. Only the claude and codex backends implement it.
type Fixer interface {
	Fix(context.Context, Request) (Result, error)
}

// MaxFixSummaryBytes bounds the summary a Fixer returns.
const MaxFixSummaryBytes = 16 << 10
