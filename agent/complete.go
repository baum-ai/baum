package agent

import (
	"context"
	"log/slog"
)

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
	// Logger, when set, receives a bounded copy of each reply the harness
	// rejected against the schema (claude), for the private log.
	Logger *slog.Logger
}

// Completion is a Completer's reply.
type Completion struct {
	// Document is the reply object, expected to match the request's Schema.
	Document []byte
	// Model is the model the provider reported, else the configured one.
	Model string
	Usage Usage
	// Rejections counts the replies the harness rejected against the schema
	// before the last one (claude).
	Rejections int
}
