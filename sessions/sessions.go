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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/claudesession"
	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/codexsession"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/entire"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/cursorsession"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/vendors"
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
	// StartedBefore drops sessions their agent's own store says began after
	// it. A session whose start discovery does not know (Cursor) is kept,
	// and Entire checkpoints are not filtered by it.
	StartedBefore time.Time
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
	// has none (checkpoints held only in refs/entire/checkpoints/* leave it
	// empty).
	EntireCommit string
}

// Load returns every session recorded for the git repository containing dir,
// mapped and ordered by modification time.
//
// A session belongs to the repository when it was recorded in any of the
// clone's working trees, the main checkout or a linked worktree, whichever of
// them dir is in; git's own list of worktrees says which those are. Each
// Entire session is returned once, from the newest checkpoint that recorded
// it, and not at all when the agent's own store holds it.
func Load(ctx context.Context, dir string, opts Options) ([]Session, Report, error) {
	d, err := Discover(ctx, dir, opts)
	if err != nil {
		return nil, Report{}, err
	}
	return d.Load(ctx)
}

// Discovery is what Load finds before it reads any session: the clone's
// worktrees, the session files recorded in them and, when Entire is read, the
// checkpoint store's branch commit and refs. Finding them reads directories,
// file metadata and git refs, never a transcript, so a caller can tell from
// Fingerprint whether anything Load reads changed before paying for it.
type Discovery struct {
	dir       string
	opts      Options
	worktrees []string
	wanted    []collector.Session
	skipped   []model.SkippedFile
	// entire describes the checkpoint store; empty when Entire is not read
	// or the clone has none.
	entire string
}

// Discover finds what Load would read for the repository containing dir.
func Discover(ctx context.Context, dir string, opts Options) (*Discovery, error) {
	d := &Discovery{dir: dir, opts: opts}
	// A clone whose worktrees git cannot list (git older than 2.36) is read
	// as the one checkout containing dir.
	if listed, err := gitlocal.Worktrees(ctx, dir); err == nil {
		for _, worktree := range listed {
			d.worktrees = append(d.worktrees, worktree.Path)
		}
	}
	found, scan, err := collector.FindSessions(dir, collector.FindOptions{
		ClaudeProjectsDir: opts.ClaudeProjectsDir,
		CodexSessionsDir:  opts.CodexSessionsDir,
		CursorStorePaths:  opts.CursorStorePaths,
		PreviousPaths:     opts.PreviousPaths,
		Worktrees:         d.worktrees,
		Since:             opts.Since,
	})
	if err != nil {
		return nil, err
	}
	d.skipped = scan.Skipped
	for _, s := range found {
		startedLater := !opts.StartedBefore.IsZero() && !s.StartedAt.IsZero() && s.StartedAt.After(opts.StartedBefore)
		if opts.wants(s.Source) && (opts.Since.IsZero() || !s.ModifiedAt.Before(opts.Since)) && !startedLater {
			d.wanted = append(d.wanted, s)
		}
	}
	if opts.wants(Entire) {
		if root, err := collector.RepoRoot(dir); err == nil {
			d.entire = entireState(ctx, root)
		}
	}
	return d, nil
}

// entireState describes the clone's Entire checkpoint stores: the branch
// commit and every checkpoint ref, or why they could not be listed, so a
// different listing gives a different description.
func entireState(ctx context.Context, root string) string {
	store, ok, err := entire.Resolve(ctx, root)
	switch {
	case err != nil:
		return "error: " + err.Error()
	case !ok:
		return ""
	}
	ids := make([]string, 0, len(store.Refs))
	for id := range store.Refs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var b strings.Builder
	b.WriteString(store.Branch)
	for _, id := range ids {
		fmt.Fprintf(&b, "\x00%s=%s", id, store.Refs[id])
	}
	return b.String()
}

// Worktrees are the clone's working trees git listed, empty when it could
// not list them.
func (d *Discovery) Worktrees() []string { return slices.Clone(d.worktrees) }

// Fingerprint describes everything Load would read: the worktrees, each
// session file's path, size and modification time (or the hash of
// generated content), and the Entire store's branch commit and refs. Two
// discoveries with the same fingerprint load the same sessions. ok is false
// when a file cannot be described, and nothing should be assumed unchanged.
func (d *Discovery) Fingerprint() (string, bool) {
	h := sha256.New()
	fmt.Fprintf(h, "%q\x00%q\x00", d.worktrees, d.entire)
	for _, s := range d.wanted {
		key, _, ok := mapKey(s)
		if !ok {
			return "", false
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%q\x00%s\x01", s.Source, s.SessionID, s.Path, s.RepoRoot, s.WorktreeRoot, s.ModifiedAt.UnixNano(), s.Warnings, key)
		// A Codex thread's name comes from the index beside its rollouts.
		if index := codextranscript.IndexPath(s.Path); s.Source == vendors.Codex && index != "" {
			if info, err := os.Stat(index); err == nil {
				fmt.Fprintf(h, "%s\x00%d\x00%d\x01", index, info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// Load maps the discovered sessions and reads the Entire checkpoints, as
// Load does.
func (d *Discovery) Load(ctx context.Context) ([]Session, Report, error) {
	workstats.Inc("sessions.load.calls")
	opts, wanted := d.opts, d.wanted
	report := Report{Skipped: d.skipped}
	mapped, err := mapAll(ctx, wanted)
	if err != nil {
		return nil, report, err
	}
	var sessions []Session
	native := map[string]bool{}
	for i, s := range wanted {
		if mapped[i].err != nil {
			report.Failures = append(report.Failures, Failure{Source: s.Source, Path: s.Path, Err: mapped[i].err})
			continue
		}
		native[s.SessionID] = true
		sessions = append(sessions, Session{
			Source: s.Source, Vendor: s.Source.String(), ID: s.SessionID, Path: s.Path,
			RepoRoot: s.RepoRoot, WorktreeRoot: s.WorktreeRoot, ModifiedAt: s.ModifiedAt,
			Graph: mapped[i].graph, Notes: mapped[i].notes,
		})
	}

	if opts.wants(Entire) {
		root, err := collector.RepoRoot(d.dir)
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
		if c := strings.Compare(string(a.Source), string(b.Source)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return sessions, report, nil
}

// ErrUnbalanced reports a tree whose mapper could not account for every
// record it read. Such a tree understates what happened and is not returned.
var ErrUnbalanced = errors.New("mapper did not account for every record")

// Map turns one discovered session into its graph. The notes are the
// mapper's remarks; an error means the tree must not be used.
//
// A session whose files have not changed since it was last mapped is not
// read again: the graph comes from a process-wide cache (see mapCache) and
// is shared with every other caller that mapped the same files, so callers
// must not change it.
func Map(s collector.Session) (*session.Graph, []string, error) {
	workstats.Inc("sessions.map.calls")
	key, cost, cacheable := mapKey(s)
	if cacheable {
		if entry, ok := cache.get(key); ok {
			return entry.graph, mapNotes(s, entry), entry.err
		}
	}
	workstats.Inc("sessions.map.decodes." + string(s.Source))
	workstats.Add("sessions.map.decoded_bytes", cost)
	graph, notes, err := mapSession(s)
	entry := &mapEntry{key: key, graph: graph, notes: notes, err: err, cost: cost}
	// A file that could not be opened or read may read next time (too many
	// open files, a permission fixed); only what its content caused is kept.
	var pathErr *fs.PathError
	if cacheable && !errors.As(err, &pathErr) {
		cache.put(entry)
	}
	return graph, mapNotes(s, entry), err
}

// mapNotes are the discovery warnings followed by the mapper's notes, or none
// when the session did not map.
func mapNotes(s collector.Session, entry *mapEntry) []string {
	if entry.err != nil {
		return nil
	}
	return slices.Concat(s.Warnings, entry.notes)
}

// mapSession reads and maps the session's files; its notes are the mapper's
// alone.
func mapSession(s collector.Session) (*session.Graph, []string, error) {
	switch s.Source {
	case vendors.ClaudeCode:
		tree, err := claudetranscript.DecodeLocalFiles(model.Files(s.Files))
		if err != nil {
			return nil, nil, fmt.Errorf("reading Claude Code transcripts: %w", err)
		}
		// Nothing reads the transcript's surveyed shape here, and the survey
		// re-reads every line, so it is skipped.
		graph, report, err := claudesession.BuildWith(tree, claudesession.BuildOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("mapping Claude Code session: %w", err)
		}
		if !report.Lines.Balanced() {
			return nil, nil, fmt.Errorf("%w: %d of %d transcript lines", ErrUnbalanced, report.Lines.Accounted(), report.Lines.Total)
		}
		return graph, report.Notes, nil
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
		return graph, report.Notes, nil
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
		return graph, report.Notes, nil
	default:
		return nil, nil, fmt.Errorf("no mapper for %q sessions", s.Source)
	}
}

// HasTranscript reports whether a graph records anything the agent did: a
// model call, an assistant message or a tool use. A snapshot Entire wrote with
// only its metadata and the prompt has none.
func HasTranscript(graph *session.Graph) bool {
	return graph != nil &&
		len(graph.ModelInvocations)+len(graph.AssistantMessages)+len(graph.ToolUses) > 0
}

// Fuller reports whether a is a better reading of a session than b: one with
// a transcript over one without, and otherwise the one written later.
func Fuller(a, b Session) bool {
	if ha, hb := HasTranscript(a.Graph), HasTranscript(b.Graph); ha != hb {
		return ha
	}
	return a.ModifiedAt.After(b.ModifiedAt)
}
