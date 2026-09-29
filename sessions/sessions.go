// Package sessions finds the coding-agent sessions recorded for a repository
// on this machine and maps each into the normalised session.Graph.
//
// Claude Code, Codex and Cursor sessions are read from their stores in the
// home directory; Entire sessions from the checkpoint branch in the clone.
// Each vendor's parser lives in its own package; this one only decides which
// stores to read and which trees are trustworthy enough to return.
package sessions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lastpersonlabs/baum/sessions/claudesession"
	"github.com/lastpersonlabs/baum/sessions/claudetranscript"
	"github.com/lastpersonlabs/baum/sessions/codexsession"
	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/collector"
	"github.com/lastpersonlabs/baum/sessions/collector/entire"
	"github.com/lastpersonlabs/baum/sessions/collector/model"
	"github.com/lastpersonlabs/baum/sessions/cursorsession"
	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/entirecheckpoint"
	"github.com/lastpersonlabs/baum/sessions/entiresession"
	"github.com/lastpersonlabs/baum/sessions/session"
	"github.com/lastpersonlabs/baum/sessions/vendors"
)

// Entire is the source slug of sessions read from Entire checkpoints. It is a
// recorder rather than an agent, so vendors does not list it; Session.Vendor
// names the agent that ran.
const Entire vendors.Slug = "entire"

// Sources lists every source Load reads, in the order it reads them.
var Sources = []vendors.Slug{vendors.ClaudeCode, vendors.Codex, vendors.Cursor, Entire}

// ParseSource accepts a source slug as typed on the command line.
func ParseSource(name string) (vendors.Slug, error) {
	slug := vendors.Slug(name)
	if slices.Contains(Sources, slug) {
		return slug, nil
	}
	return "", fmt.Errorf("unknown vendor %q; use one of claude_code, codex, cursor, entire", name)
}

// Options narrows what Load reads. Zero values mean everything.
type Options struct {
	// PreviousPaths are directories the repository used to live at, so
	// sessions recorded there still count.
	PreviousPaths []string
	// Sources restricts the stores read; empty means Sources.
	Sources []vendors.Slug
	// Since drops sessions last modified before it.
	Since time.Time
	// Store overrides, mainly for tests. Empty means the vendor default.
	ClaudeProjectsDir string
	CodexSessionsDir  string
	CursorStorePaths  []string
}

func (o Options) wants(source vendors.Slug) bool {
	return len(o.Sources) == 0 || slices.Contains(o.Sources, source)
}

// Session is one mapped session tree.
type Session struct {
	// Source is the store the session was read from; Vendor the agent that
	// ran. They differ for Entire, which records other agents' sessions.
	Source vendors.Slug
	Vendor string
	ID     string
	// Path is the transcript file, the Cursor store plus composer id, or the
	// Entire checkpoint id.
	Path         string
	RepoRoot     string
	WorktreeRoot string
	ModifiedAt   time.Time
	Graph        *session.Graph
	// Notes are the mapper's remarks about the tree: a subagent nothing
	// spawned, a rollout that could not be read.
	Notes []string
}

// Root is the session's top-level session row, or nil for an empty graph.
func (s Session) Root() *session.Session {
	return Root(s.Graph)
}

// Root returns the top-level session of a graph, or nil.
func Root(graph *session.Graph) *session.Session {
	if graph == nil {
		return nil
	}
	for i := range graph.Sessions {
		if graph.Sessions[i].TopLevel() {
			return &graph.Sessions[i]
		}
	}
	return nil
}

// Failure is one session discovery found but could not map. It never costs
// its siblings: Load returns the sessions it could map alongside these.
type Failure struct {
	Source vendors.Slug
	Path   string
	Err    error
}

// Report is what Load dropped along the way.
type Report struct {
	// Skipped are files discovery could not read.
	Skipped []model.SkippedFile
	// Failures are sessions that were found but did not map.
	Failures []Failure
	// EntireCommit is the checkpoint branch commit read, empty when the clone
	// has none.
	EntireCommit string
}

// Load returns every session recorded for the git repository containing dir,
// mapped and ordered by modification time.
func Load(ctx context.Context, dir string, opts Options) ([]Session, Report, error) {
	var report Report
	found, scan, err := collector.FindSessions(dir, collector.FindOptions{
		ClaudeProjectsDir: opts.ClaudeProjectsDir,
		CodexSessionsDir:  opts.CodexSessionsDir,
		CursorStorePaths:  opts.CursorStorePaths,
		PreviousPaths:     opts.PreviousPaths,
	})
	if err != nil {
		return nil, report, err
	}
	report.Skipped = scan.Skipped

	var sessions []Session
	native := map[string]bool{}
	for _, s := range found {
		if !opts.wants(s.Source) || (!opts.Since.IsZero() && s.ModifiedAt.Before(opts.Since)) {
			continue
		}
		graph, notes, err := Map(s)
		if err != nil {
			report.Failures = append(report.Failures, Failure{Source: s.Source, Path: s.Path, Err: err})
			continue
		}
		native[s.SessionID] = true
		sessions = append(sessions, Session{
			Source: s.Source, Vendor: s.Source.String(), ID: s.SessionID, Path: s.Path,
			RepoRoot: s.RepoRoot, WorktreeRoot: s.WorktreeRoot, ModifiedAt: s.ModifiedAt,
			Graph: graph, Notes: notes,
		})
	}

	if opts.wants(Entire) {
		root, err := collector.RepoRoot(dir)
		if err != nil {
			return nil, report, err
		}
		// A clone git cannot read still has its home-directory sessions, so a
		// checkpoint branch that cannot be listed is reported, not fatal.
		fromEntire, commit, failures, err := loadEntire(ctx, root, opts, native)
		if err != nil {
			failures = append(failures, Failure{Source: Entire, Path: root, Err: err})
		}
		report.EntireCommit = commit
		report.Failures = append(report.Failures, failures...)
		sessions = append(sessions, fromEntire...)
	}

	slices.SortStableFunc(sessions, func(a, b Session) int {
		if c := a.ModifiedAt.Compare(b.ModifiedAt); c != 0 {
			return c
		}
		if a.Source != b.Source {
			return -1
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	return sessions, report, nil
}

// ErrUnbalanced reports a tree whose mapper could not account for every
// record it read. Such a tree understates what happened and is not returned.
var ErrUnbalanced = errors.New("mapper did not account for every record")

// Map turns one discovered session into its graph. The notes are the
// mapper's remarks; an error means the tree must not be used.
func Map(s collector.Session) (*session.Graph, []string, error) {
	switch s.Source {
	case vendors.ClaudeCode:
		tree, err := claudetranscript.DecodeLocalFiles(model.Files(s.Files))
		if err != nil {
			return nil, nil, fmt.Errorf("reading Claude Code transcripts: %w", err)
		}
		graph, report, err := claudesession.Build(tree)
		if err != nil {
			return nil, nil, fmt.Errorf("mapping Claude Code session: %w", err)
		}
		if !report.Lines.Balanced() {
			return nil, nil, fmt.Errorf("%w: %d of %d transcript lines", ErrUnbalanced, report.Lines.Accounted(), report.Lines.Total)
		}
		return graph, append(s.Warnings, report.Notes...), nil
	case vendors.Codex:
		tree, err := codextranscript.DecodeLocalFiles(model.Files(s.Files))
		if err != nil {
			return nil, nil, fmt.Errorf("reading Codex rollouts: %w", err)
		}
		graph, report, err := codexsession.Build(tree)
		if err != nil {
			return nil, nil, fmt.Errorf("mapping Codex session: %w", err)
		}
		switch {
		case !report.Records.Balanced():
			return nil, nil, fmt.Errorf("%w: %d of %d rollout records", ErrUnbalanced, report.Records.Accounted(), report.Records.Total)
		case report.UnsupportedTurns > 0:
			return nil, nil, fmt.Errorf("%w: %d unsupported turns", ErrUnbalanced, report.UnsupportedTurns)
		}
		return graph, append(s.Warnings, report.Notes...), nil
	case vendors.Cursor:
		if len(s.Files) == 0 || s.Files[0].Data == nil {
			return nil, nil, errors.New("the Cursor session carries no composer tree")
		}
		tree, err := cursortranscript.DecodeTreePayload(s.Files[0].Data)
		if err != nil {
			return nil, nil, fmt.Errorf("reading Cursor tree: %w", err)
		}
		graph, report, err := cursorsession.Build(tree)
		if err != nil {
			return nil, nil, fmt.Errorf("mapping Cursor session: %w", err)
		}
		if !report.Bubbles.Balanced() {
			return nil, nil, fmt.Errorf("%w: %d of %d Cursor bubbles", ErrUnbalanced, report.Bubbles.Accounted(), report.Bubbles.Total)
		}
		return graph, append(s.Warnings, report.Notes...), nil
	default:
		return nil, nil, fmt.Errorf("no mapper for %q sessions", s.Source)
	}
}

// loadEntire maps every checkpoint in the clone's checkpoint branch. A
// session Entire recorded that discovery already found in the agent's own
// store is skipped: the native transcript is the fuller record.
func loadEntire(ctx context.Context, root string, opts Options, native map[string]bool) ([]Session, string, []Failure, error) {
	commit, ok, err := entire.Resolve(ctx, root)
	if err != nil || !ok {
		return nil, "", nil, err
	}
	checkpoints, err := entire.List(ctx, root, commit)
	if err != nil {
		return nil, commit, nil, err
	}
	var sessions []Session
	var failures []Failure
	for _, checkpoint := range checkpoints {
		fsys, err := entire.Read(ctx, root, commit, checkpoint.ID)
		if err != nil {
			failures = append(failures, Failure{Source: Entire, Path: checkpoint.ID, Err: err})
			continue
		}
		parsed, err := entirecheckpoint.ParseCheckpoint(fsys)
		if err != nil {
			failures = append(failures, Failure{Source: Entire, Path: checkpoint.ID, Err: err})
			continue
		}
		results, _, err := entiresession.Build(parsed)
		if err != nil {
			failures = append(failures, Failure{Source: Entire, Path: checkpoint.ID, Err: err})
			continue
		}
		for _, result := range results {
			switch {
			case result.Err != nil:
				failures = append(failures, Failure{Source: Entire, Path: checkpoint.ID + "/" + result.SessionID, Err: result.Err})
				continue
			case result.Graph == nil || native[result.SessionID]:
				continue
			case !opts.Since.IsZero() && result.CreatedAt.Before(opts.Since):
				continue
			}
			sessions = append(sessions, Session{
				Source: Entire, Vendor: result.Vendor, ID: result.SessionID, Path: checkpoint.ID,
				RepoRoot: root, ModifiedAt: result.CreatedAt, Graph: result.Graph, Notes: result.Warnings,
			})
		}
	}
	return sessions, commit, failures, nil
}
