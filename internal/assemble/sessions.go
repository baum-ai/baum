package assemble

import (
	"context"
	"io"

	"github.com/lastpersonlabs/baum/bundle"
	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/internal/locallink"
	"github.com/lastpersonlabs/baum/sessions"
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

// Evidence implements Sessions.
func (l LocalSessions) Evidence(ctx context.Context, commits []gitlocal.Commit, maxPrompts, maxCommandEvents int) ([]bundle.Session, error) {
	return locallink.BuildSessionEvidence(ctx, locallink.EvidenceRequest{
		Dir: l.Dir, Commits: commits, Sessions: l.Stores,
		MaxPromptsPerSession: maxPrompts, MaxCommandEventsPerSession: maxCommandEvents, Warn: l.Warn,
	})
}
