package sessioncorrelate

import (
	"github.com/lastpersonlabs/baum/sessions/session"
	"github.com/lastpersonlabs/baum/sessions/sourcepath"
)

// PathResolver makes individual file touches repo-relative by the same rules
// Correlate applies to its file evidence: the touch's own session cwd values,
// then the rest of the tree's (longest wins in each), then the anchor rule.
// Touches that WithRepositoryRoots moves to another repository do not
// resolve. Consumers that need per-touch paths, not the tree's deduplicated
// file list, use it so the two views cannot disagree about a path.
type PathResolver struct {
	dirs    workingDirs
	anchors []string
	moved   map[string]bool
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
	return r
}

// Touch returns the repo-relative path of touch, or false when nothing
// relativizes it (for example a plan file outside the repository) or it
// belongs to another repository.
func (r *PathResolver) Touch(touch session.FileTouch) (string, bool) {
	if r.moved[touch.ID] {
		return "", false
	}
	return repoRelative(cleanPath(touch.Path), r.dirs.of(touch.SessionID), r.dirs.pooled.values(), r.anchors)
}
