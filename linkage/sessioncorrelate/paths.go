package sessioncorrelate

import (
	"github.com/baumai/baum/sessions/session"
	"github.com/baumai/baum/sessions/sourcepath"
)

// PathResolver makes individual file touches repo-relative by the same rules
// Correlate applies to its file evidence: recorded repository and worktree
// roots, then the touch's own session cwd values, then the rest of the
// tree's (longest wins in each), never cutting a cwd-relative path deeper
// than an in-repo agent worktree the tree worked in, then the anchor rule,
// with a leading agent worktree removed. Touches that WithRepositoryRoots moves to another
// repository do not resolve.
// Consumers that need per-touch paths, not the tree's deduplicated file list,
// use it so the two views cannot disagree about a path.
//
// WithCloneRoots narrows it further for a caller that knows the checkouts the
// repository was worked in: an absolute path then resolves only inside one of
// them, cut at the longest and with a leading agent worktree removed, and a
// relative one only once joined with the working directory it was recorded
// in, so a file beside the clone is never mistaken for one in it.
type PathResolver struct {
	dirs       workingDirs
	anchors    []string
	moved      map[string]bool
	cloneRoots []string
}

// NewPathResolver prepares relativization for graph. vendor and opts mean
// what they mean for Correlate; WithEditedFiles paths only anchor here.
func NewPathResolver(graph *session.Graph, vendor string, opts ...Option) *PathResolver {
	if graph == nil {
		graph = &session.Graph{}
	}
	settings := options{}
	for _, opt := range opts {
		opt(&settings)
	}
	keys := keysFor(vendor)
	r := &PathResolver{dirs: collectDirs(graph, keys), anchors: relativeTouchPaths(graph)}
	for _, reported := range append(append([]string{}, settings.fileAnchors...), settings.editedFiles...) {
		if path := cleanPath(reported); sourcepath.IsRepositoryRelative(path) {
			r.anchors = append(r.anchors, path)
		}
	}
	classifier := newRepositoryClassifier(settings.repositoryRootsSet, settings.primaryRepository, settings.repositoryRoots)
	_, r.moved, _, _, _ = classifier.classify(graph, r.dirs, keys)
	for _, root := range settings.cloneRoots {
		if parsed := sourcepath.Parse(cleanPath(root)); parsed.Valid() && parsed.IsAbsolute() {
			r.cloneRoots = append(r.cloneRoots, parsed.String())
		}
	}
	return r
}

// WithCloneRoots gives PathResolver the absolute directories of the clone's
// checkouts (its main working tree, its worktrees and the directories it used
// to live at). Correlate ignores it.
func WithCloneRoots(roots ...string) Option {
	return func(o *options) { o.cloneRoots = append(o.cloneRoots, roots...) }
}

// Touch returns the repo-relative path of touch, or false when nothing
// relativizes it (for example a plan file outside the repository) or it
// belongs to another repository.
func (r *PathResolver) Touch(touch session.FileTouch) (string, bool) {
	if r.moved[touch.ID] {
		return "", false
	}
	path := cleanPath(touch.Path)
	parsed := sourcepath.Parse(path)
	if len(r.cloneRoots) == 0 || !parsed.Valid() {
		return r.dirs.repoRelative(path, touch.SessionID, r.anchors)
	}
	if parsed.IsAbsolute() {
		return r.inClone(parsed.String())
	}
	// A relative path is relative to the working directory: the session's
	// own, else the tree's. It is kept only when every one of them puts it
	// at the same place in the clone; a tree that recorded none is read as
	// the hosted service reads it.
	cwds := r.dirs.recordedOf(touch.SessionID)
	if len(cwds) == 0 {
		cwds = r.dirs.recorded.values()
	}
	if len(cwds) == 0 {
		return r.dirs.repoRelative(path, touch.SessionID, r.anchors)
	}
	resolved := ""
	for _, cwd := range cwds {
		joined := sourcepath.Parse(sourcepath.Join(cwd, parsed.String()))
		if !joined.IsAbsolute() {
			return "", false
		}
		relative, ok := r.inClone(joined.String())
		if !ok || (resolved != "" && relative != resolved) {
			return "", false
		}
		resolved = relative
	}
	return resolved, true
}

// inClone cuts an absolute path at the longest clone root holding it and
// removes a leading agent worktree, or reports false outside every root.
func (r *PathResolver) inClone(path string) (string, bool) {
	relative, ok := stripLongestDir(path, r.cloneRoots)
	if !ok {
		return "", false
	}
	return withoutAgentWorktree(relative)
}

// Dirs returns every working directory and recorded root in the tree.
func (r *PathResolver) Dirs() []string {
	return r.dirs.pooled.values()
}

// Roots returns the tree's recorded repository and worktree roots, which
// relativization tries before any working directory.
func (r *PathResolver) Roots() []string {
	return r.dirs.roots.values()
}

// WithoutAgentWorktree removes a leading agent worktree such as
// {.claude,.agents}/worktrees/<name>/ from a repo-relative path, as
// relativization does,
// or returns false when nothing is left. Callers use it to compare paths
// stored before that prefix was removed.
func WithoutAgentWorktree(path string) (string, bool) {
	return withoutAgentWorktree(path)
}
