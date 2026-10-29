package locallink

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// LinkOptions say where LinkCommits looks.
type LinkOptions struct {
	// Repository is the commits' "owner/name"; the clone's remote naming it
	// is the one a missing Entire checkpoint is fetched from.
	Repository string
	// Sessions and Warn are as in Request.
	Sessions sessions.Options
	Warn     io.Writer
}

// CommitLink is one session matched to a commit.
type CommitLink struct {
	Commit string
	// Session is the session's id and Vendor the agent that ran it.
	Session, Vendor string
	Method          string
	Confidence      float64
}

// LinkCommits matches the commits, of any pull requests of the clone
// containing dir, to the sessions recorded for that clone, exactly as
// Resolve matches a pull request's commits: by their Entire checkpoints,
// then the matcher's default steps, baum's own generation runs left out and
// only sessions written within SessionWindow of the commits read. Nothing is
// redacted: the links carry ids only.
func LinkCommits(ctx context.Context, dir string, commits []gitlocal.Commit, opts LinkOptions) ([]CommitLink, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	// The remote is only a missing checkpoint's fetch hint in a warning: the
	// clone is opened for it only when warnings are read and a commit names
	// a checkpoint.
	remote := ""
	if opts.Repository != "" && opts.Warn != nil && slices.ContainsFunc(commits, func(c gitlocal.Commit) bool {
		valid, _ := entirecheckpoint.ParseMessage(c.Message)
		return len(valid) > 0
	}) {
		repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{})
		if err != nil {
			return nil, fmt.Errorf("opening the clone: %w", err)
		}
		remotes, err := repo.RemoteURLs(ctx)
		_ = repo.Close()
		if err != nil {
			return nil, fmt.Errorf("reading the clone's remotes: %w", err)
		}
		remote = remoteFor(remotes, opts.Repository)
	}
	found, truth, err := loadSessions(ctx, dir, opts.Sessions, commits, remote, opts.Warn)
	if err != nil {
		return nil, err
	}
	_, latest, edges, err := linkSessions(ctx, found, commits, truth)
	if err != nil {
		return nil, err
	}
	links := make([]CommitLink, 0, len(edges))
	for _, edge := range edges {
		links = append(links, CommitLink{Commit: edge.CommitSHA, Session: edge.RunID, Vendor: latest[edge.RunID].session.Vendor,
			Method: string(edge.Method), Confidence: edge.Confidence})
	}
	return links, nil
}
