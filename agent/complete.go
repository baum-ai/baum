package agent

import "context"

// Completer is a backend that can make a single structured-output model call
// without tools, files or a working directory: the system prompt, one user
// message and the JSON Schema the reply must match. Decision capture uses it.
// The claude, codex and anthropic backends implement it; cursor-agent does
// not.
type Completer interface {
	// Complete makes the call. On an error the Completion still carries the
	// model and the usage the provider reported, when the call reached it, so
	// the caller can account for what it may have been billed.
	Complete(context.Context, CompleteRequest) (Completion, error)
}

// CompleteRequest is one Completer call.
type CompleteRequest struct {
	System, User string
	// Schema is the reply's JSON Schema.
	Schema []byte
	// MaxOutputTokens caps the reply; zero is the backend's default.
	MaxOutputTokens int
}

// Completion is a Completer's reply.
type Completion struct {
	// Document is the reply object, expected to match the request's Schema.
	Document []byte
	// Model is the model the provider reported, else the configured one.
	Model string
	Usage Usage
	// Rejections counts the replies the harness rejected against the schema
	// (claude), and RejectionDetail is a bounded copy of the last one's
	// reason, which describes the reply's shape, for the private log.
	Rejections      int
	RejectionDetail string
}
