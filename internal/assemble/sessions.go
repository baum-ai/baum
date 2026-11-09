package assemble

import (
	"context"
	"io"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/sessions"
)

// LocalSessions finds the sessions recorded on this machine for the clone
// containing Dir, through locallink.BuildSessionEvidence.
type LocalSessions struct {
	// Dir is the clone, or a directory inside it.
	Dir string
	// Stores narrows or redirects the session stores, and carries the
	// directories the repository used to live at.
	Stores sessions.Options
	// Warn receives a line for each store file that could not be read.
	Warn io.Writer
}

// FrozenSessions is a Sessions that can answer for a pull request that is
// closed or merged with the evidence it last built for the same commits,
// without looking at the session stores again.
type FrozenSessions interface {
	Sessions
	FrozenEvidence(ctx context.Context, commits []gitlocal.Commit, maxPrompts, maxCommandEvents int) ([]bundle.Session, error)
}

// Evidence implements Sessions.
func (l LocalSessions) Evidence(ctx context.Context, commits []gitlocal.Commit, maxPrompts, maxCommandEvents int) ([]bundle.Session, error) {
	return locallink.BuildSessionEvidence(ctx, l.request(commits, maxPrompts, maxCommandEvents, false))
}

// FrozenEvidence implements FrozenSessions.
func (l LocalSessions) FrozenEvidence(ctx context.Context, commits []gitlocal.Commit, maxPrompts, maxCommandEvents int) ([]bundle.Session, error) {
	return locallink.BuildSessionEvidence(ctx, l.request(commits, maxPrompts, maxCommandEvents, true))
}

func (l LocalSessions) request(commits []gitlocal.Commit, maxPrompts, maxCommandEvents int, frozen bool) locallink.EvidenceRequest {
	return locallink.EvidenceRequest{
		Dir: l.Dir, Commits: commits, Sessions: l.Stores,
		MaxPromptsPerSession: maxPrompts, MaxCommandEventsPerSession: maxCommandEvents, Warn: l.Warn,
		Frozen: frozen,
	}
}
