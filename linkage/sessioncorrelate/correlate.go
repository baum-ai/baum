// Package sessioncorrelate derives the evidence the commit matcher needs from a
// session tree: repo-relative file paths, role-tagged commit refs, and the
// branches and working directories the tree was seen in.
//
// It never mutates the graph. Rewriting file paths inside the session would
// make the stored session stop saying what the tool actually reported, and
// every consumer would inherit the matcher's view of a path. Vendor truth stays
// in the session package and the matcher-shaped view is a separate value the
// ingest layer stores beside it.
//
// The package is pure: no I/O, no database, and no mapper packages. The only
// vendor knowledge in it is the attribute-key table in keys.go, a
// per-vendor hook keyed by slug.
package sessioncorrelate

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/textclean"
	"github.com/baum-ai/baum/linkage/gitevidence"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/sourcepath"
)

// Role is how a session relates to a commit SHA: created it, mentioned it,
// checked it out, and so on. It is defined here rather than imported from the
// matcher or a session store so this package stays decoupled from both; the
// ingest layer translates at the persist boundary.
type Role string

const (
	RoleCreated    Role = "created"
	RoleMentioned  Role = "mentioned"
	RoleCheckedOut Role = "checked_out"
	RoleStarting   Role = "starting"
)

// FileEvidence is one repo-relative path the tree touched, and how.
type FileEvidence struct {
	Path string
	Kind session.TouchKind
}

// CommitRef is one role-tagged commit SHA the tree evidenced. SHA is lowercase:
// see commitRefSet.add.
type CommitRef struct {
	SHA        string
	Role       Role
	ObservedAt *time.Time
	// ToolUseID owns the observation when a tool call produced it, and is nil
	// for evidence that came from session metadata rather than from a tool.
	ToolUseID *string
}

// Result is everything the matcher may read about a session tree. Fail closed:
// an input this package cannot confidently interpret becomes a warning, never
// an invented file, branch, or commit ref.
type Result struct {
	Files                  []FileEvidence
	CommitRefs             []CommitRef
	Branches               []string
	WorkingDirs            []string
	Warnings               []string
	AdditionalRepositories []RepositoryResult
}

// RepositoryResult is matcher evidence proven to belong to one additional
// repository.
type RepositoryResult struct {
	RepositoryID string
	Files        []FileEvidence
	CommitRefs   []CommitRef
	Branches     []string
	WorkingDirs  []string
}

// RepositoryRoots binds verified roots to a repository for one upload.
type RepositoryRoots struct {
	RepositoryID string
	Roots        []string
}

// Option supplies evidence the source recorded beside the graph rather than
// inside it.
type Option func(*options)

type options struct {
	editedFiles        []string
	fileAnchors        []string
	repositoryRootsSet bool
	primaryRepository  string
	repositoryRoots    []RepositoryRoots
	cloneRoots         []string
}

// WithEditedFiles adds repository-relative paths the source reported as
// changed while this session ran — Entire's session metadata files_touched.
// They become `edit` evidence in their own right, and they anchor
// relativization for a session that recorded no working directory.
//
// The list is a working-tree diff, not a record of the session's tool calls,
// so the caller decides when it may stand as evidence. The Entire ingest
// supplies it only for a session with no native file evidence of its own, and
// never when a sibling session in the checkpoint reports the identical list.
func WithEditedFiles(paths ...string) Option {
	return func(o *options) { o.editedFiles = append(o.editedFiles, paths...) }
}

// WithFileAnchors supplies repository-relative paths only for relativizing
// native file touches; the paths do not become evidence themselves.
func WithFileAnchors(paths ...string) Option {
	return func(o *options) { o.fileAnchors = append(o.fileAnchors, paths...) }
}

// WithRepositoryRoots enables strict cross-repository classification through
// roots already validated and authorized by the ingest layer.
func WithRepositoryRoots(primaryRepository string, repositories ...RepositoryRoots) Option {
	return func(o *options) {
		o.repositoryRootsSet = true
		o.primaryRepository = primaryRepository
		o.repositoryRoots = append(o.repositoryRoots, repositories...)
	}
}

// Correlate reads graph and returns its matcher evidence. graph is not
// modified. vendor selects the attribute spellings in keys.go; an unrecognised
// slug falls back to the union of every vendor's.
func Correlate(graph *session.Graph, vendor string, opts ...Option) Result {
	if graph == nil {
		return Result{}
	}
	settings := options{}
	for _, opt := range opts {
		opt(&settings)
	}
	keys := keysFor(vendor)
	dirs := collectDirs(graph, keys)
	classifier := newRepositoryClassifier(
		settings.repositoryRootsSet, settings.primaryRepository, settings.repositoryRoots,
	)
	additional, movedTouches, movedSessions, movedDirs, classificationWarnings := classifier.classify(graph, dirs, keys)
	result := Result{
		Branches:               collectBranches(graph, keys, movedSessions),
		WorkingDirs:            filterMovedDirs(dirs.recorded.values(), movedDirs),
		AdditionalRepositories: additional,
	}
	result.Files, result.Warnings = fileEvidence(graph, dirs, settings.editedFiles, settings.fileAnchors, movedTouches)
	result.Warnings = append(result.Warnings, classificationWarnings...)
	for _, ref := range commitRefs(graph, keys) {
		repositoryID := ""
		if ref.ToolUseID != nil {
			for _, use := range graph.ToolUses {
				if use.ID == *ref.ToolUseID {
					repositoryID = movedSessions[use.SessionID]
					break
				}
			}
		} else if root := graph.Root(); root != nil {
			repositoryID = movedSessions[root.ID]
		}
		if repositoryID == "" {
			result.CommitRefs = append(result.CommitRefs, ref)
			continue
		}
		for i := range result.AdditionalRepositories {
			if result.AdditionalRepositories[i].RepositoryID == repositoryID {
				result.AdditionalRepositories[i].CommitRefs = append(result.AdditionalRepositories[i].CommitRefs, ref)
				break
			}
		}
	}
	additional = result.AdditionalRepositories[:0]
	for _, repository := range result.AdditionalRepositories {
		if len(repository.Files)+len(repository.CommitRefs)+len(repository.Branches)+len(repository.WorkingDirs) > 0 {
			additional = append(additional, repository)
		}
	}
	result.AdditionalRepositories = additional
	return result
}

// workingDirs holds a tree's cwd history and its recorded repository/worktree
// roots, both per session and pooled. Only cwd values are exposed as evidence.
// The per-session split matters for relativization: a session that ran in
// /repo must not have its own paths cut against a sibling's deeper
// /repo/platform.
type workingDirs struct {
	recorded          *stringSet
	pooled            *stringSet
	bySession         map[string]*stringSet
	recordedBySession map[string]*stringSet
	// roots and rootsBySession hold only recorded repository and worktree
	// roots, which relativization tries before any cwd.
	roots          *stringSet
	rootsBySession map[string]*stringSet
}

func (w workingDirs) rootsOf(sessionID string) []string {
	if set, known := w.rootsBySession[sessionID]; known {
		return set.values()
	}
	return nil
}

func (w workingDirs) recordedOf(sessionID string) []string {
	if set, known := w.recordedBySession[sessionID]; known {
		return set.values()
	}
	return nil
}

func (w workingDirs) of(sessionID string) []string {
	if set, known := w.bySession[sessionID]; known {
		return set.values()
	}
	return nil
}

func collectDirs(graph *session.Graph, keys attributeKeys) workingDirs {
	dirs := workingDirs{
		recorded: newStringSet(), pooled: newStringSet(),
		bySession: map[string]*stringSet{}, recordedBySession: map[string]*stringSet{},
		roots: newStringSet(), rootsBySession: map[string]*stringSet{},
	}
	addRoot := func(sessionID, value string) {
		if value = cleanPath(value); value == "" {
			return
		}
		set, known := dirs.rootsBySession[sessionID]
		if !known {
			set = newStringSet()
			dirs.rootsBySession[sessionID] = set
		}
		set.add(value)
		dirs.roots.add(value)
	}
	add := func(sessionID, value string, recorded bool) {
		if value = cleanPath(value); value == "" {
			return
		}
		set, known := dirs.bySession[sessionID]
		if !known {
			set = newStringSet()
			dirs.bySession[sessionID] = set
		}
		set.add(value)
		dirs.pooled.add(value)
		if recorded {
			dirs.recorded.add(value)
			set, known := dirs.recordedBySession[sessionID]
			if !known {
				set = newStringSet()
				dirs.recordedBySession[sessionID] = set
			}
			set.add(value)
		}
	}
	for i := range graph.Sessions {
		row := &graph.Sessions[i]
		if row.WorkingDir != nil {
			add(row.ID, *row.WorkingDir, true)
		}
		if row.RepoRoot != nil {
			add(row.ID, *row.RepoRoot, false)
			addRoot(row.ID, *row.RepoRoot)
		}
		if row.WorktreeRoot != nil {
			add(row.ID, *row.WorktreeRoot, false)
			addRoot(row.ID, *row.WorktreeRoot)
		}
	}
	for _, attribute := range graph.Attributes {
		if !keys.workingDir[attribute.Key] {
			continue
		}
		if value, ok := attributeString(attribute.Value); ok {
			add(attribute.SessionID, value, true)
		}
	}
	return dirs
}

func collectBranches(graph *session.Graph, keys attributeKeys, movedSessions map[string]string) []string {
	branches := newStringSet()
	for i := range graph.Sessions {
		if row := &graph.Sessions[i]; row.GitBranch != nil && movedSessions[row.ID] == "" {
			branches.add(clean(*row.GitBranch))
		}
	}
	for _, attribute := range graph.Attributes {
		if !keys.branch[attribute.Key] || movedSessions[attribute.SessionID] != "" {
			continue
		}
		if value, ok := attributeString(attribute.Value); ok {
			branches.add(clean(value))
		}
	}
	return branches.values()
}

type classifiedRoot struct {
	repositoryID string
	path         string
}

type repositoryClassifier struct {
	enabled bool
	primary string
	roots   []classifiedRoot
}

func newRepositoryClassifier(enabled bool, primary string, repositories []RepositoryRoots) repositoryClassifier {
	classifier := repositoryClassifier{enabled: enabled, primary: primary}
	owners := map[string]map[string]bool{}
	type candidate struct {
		classifiedRoot
		key string
	}
	var candidates []candidate
	for _, repository := range repositories {
		for _, root := range repository.Roots {
			parsed := sourcepath.Parse(root)
			if !parsed.Valid() || !parsed.IsAbsolute() {
				continue
			}
			key := parsed.String()
			if parsed.Style() == sourcepath.StyleWindowsDrive || parsed.Style() == sourcepath.StyleWindowsUNC {
				key = strings.ToLower(key)
			}
			if owners[key] == nil {
				owners[key] = map[string]bool{}
			}
			owners[key][repository.RepositoryID] = true
			candidates = append(candidates, candidate{
				classifiedRoot: classifiedRoot{repositoryID: repository.RepositoryID, path: parsed.String()}, key: key,
			})
		}
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if len(owners[candidate.key]) != 1 || seen[candidate.repositoryID+"\x00"+candidate.key] {
			continue
		}
		seen[candidate.repositoryID+"\x00"+candidate.key] = true
		classifier.roots = append(classifier.roots, candidate.classifiedRoot)
	}
	return classifier
}

func (c repositoryClassifier) classify(
	graph *session.Graph, dirs workingDirs, keys attributeKeys,
) ([]RepositoryResult, map[string]bool, map[string]string, map[string]bool, []string) {
	if !c.enabled {
		return nil, nil, nil, nil, nil
	}
	if c.primary == "" || len(c.roots) == 0 {
		return []RepositoryResult{}, nil, nil, nil, nil
	}

	type group struct {
		RepositoryResult
		files    map[fileKey]bool
		branches *stringSet
		dirs     *stringSet
	}
	groups := map[string]*group{}
	for _, root := range c.roots {
		if root.repositoryID == c.primary || groups[root.repositoryID] != nil {
			continue
		}
		groups[root.repositoryID] = &group{
			RepositoryResult: RepositoryResult{RepositoryID: root.repositoryID},
			files:            map[fileKey]bool{}, branches: newStringSet(), dirs: newStringSet(),
		}
	}

	movedSessions := map[string]string{}
	for _, row := range graph.Sessions {
		repositoryID := ""
		ambiguous := false
		for _, dir := range dirs.recordedOf(row.ID) {
			classified, _, ok, conflict := c.path(dir)
			if conflict || !ok {
				ambiguous = true
				break
			}
			if repositoryID != "" && repositoryID != classified {
				ambiguous = true
				break
			}
			repositoryID = classified
		}
		if !ambiguous && repositoryID != "" && repositoryID != c.primary {
			movedSessions[row.ID] = repositoryID
		}
	}

	movedDirs := map[string]bool{}
	for sessionID, recorded := range dirs.recordedBySession {
		repositoryID := movedSessions[sessionID]
		if repositoryID == "" {
			continue
		}
		for _, dir := range recorded.values() {
			classified, relative, ok, _ := c.path(dir)
			if !ok || classified != repositoryID {
				continue
			}
			if target := groups[repositoryID]; target != nil {
				target.dirs.add(relative)
				movedDirs[dir] = true
			}
		}
	}

	movedTouches := map[string]bool{}
	ambiguousPaths := 0
	for _, touch := range graph.FileTouches {
		reported := cleanPath(touch.Path)
		repositoryID, relative, ok, conflict := c.path(reported)
		if conflict {
			ambiguousPaths++
			continue
		}
		if !ok && sourcepath.IsRepositoryRelative(reported) {
			repositoryID = movedSessions[touch.SessionID]
			relative, ok = reported, repositoryID != ""
		}
		if ok {
			relative, ok = withoutAgentWorktree(relative)
		}
		if !ok || repositoryID == c.primary {
			continue
		}
		target := groups[repositoryID]
		if target == nil {
			continue
		}
		key := fileKey{path: relative, kind: touch.Kind}
		if !target.files[key] {
			target.files[key] = true
			target.Files = append(target.Files, FileEvidence{Path: relative, Kind: touch.Kind})
		}
		movedTouches[touch.ID] = true
	}

	for _, row := range graph.Sessions {
		if target := groups[movedSessions[row.ID]]; target != nil && row.GitBranch != nil {
			target.branches.add(clean(*row.GitBranch))
		}
	}
	for _, attribute := range graph.Attributes {
		if !keys.branch[attribute.Key] {
			continue
		}
		if target := groups[movedSessions[attribute.SessionID]]; target != nil {
			if value, ok := attributeString(attribute.Value); ok {
				target.branches.add(clean(value))
			}
		}
	}

	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]RepositoryResult, 0, len(ids))
	for _, id := range ids {
		entry := groups[id]
		entry.Branches = entry.branches.values()
		entry.WorkingDirs = entry.dirs.values()
		result = append(result, entry.RepositoryResult)
	}
	var warnings []string
	if ambiguousPaths > 0 {
		warnings = append(warnings, fmt.Sprintf("could not classify %d path(s) with ambiguous repository roots", ambiguousPaths))
	}
	return result, movedTouches, movedSessions, movedDirs, warnings
}

func (c repositoryClassifier) path(value string) (string, string, bool, bool) {
	parsed := sourcepath.Parse(value)
	if !parsed.Valid() || !parsed.IsAbsolute() {
		return "", "", false, false
	}
	bestLength := -1
	bestRepository, bestRelative := "", ""
	conflict := false
	for _, root := range c.roots {
		relative, ok := sourcepath.Rel(root.path, parsed.String())
		if !ok && sameRecordedPath(root.path, parsed.String()) {
			relative, ok = ".", true
		}
		if !ok || len(root.path) < bestLength {
			continue
		}
		if len(root.path) == bestLength && bestRepository != root.repositoryID {
			conflict = true
			continue
		}
		bestLength, bestRepository, bestRelative = len(root.path), root.repositoryID, relative
		conflict = false
	}
	return bestRepository, bestRelative, bestRepository != "" && !conflict, conflict
}

func sameRecordedPath(left, right string) bool {
	leftPath, rightPath := sourcepath.Parse(left), sourcepath.Parse(right)
	if leftPath.Style() != rightPath.Style() {
		return false
	}
	if leftPath.Style() == sourcepath.StyleWindowsDrive || leftPath.Style() == sourcepath.StyleWindowsUNC {
		return strings.EqualFold(leftPath.String(), rightPath.String())
	}
	return leftPath.String() == rightPath.String()
}

func filterMovedDirs(values []string, moved map[string]bool) []string {
	if len(values) == 0 {
		return values
	}
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if !moved[value] {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

type fileKey struct {
	path string
	kind session.TouchKind
}

// fileEvidence relativizes every file touch and deduplicates on (path, kind),
// which is the key stored file evidence uses. Externally reported edited
// paths are already repository-relative, so they are evidence directly and
// also anchor the touches this tree could not relativize on its own.
func fileEvidence(graph *session.Graph, dirs workingDirs, editedFiles, fileAnchors []string, movedTouches map[string]bool) ([]FileEvidence, []string) {
	anchors := relativeTouchPaths(graph)
	for _, reported := range fileAnchors {
		if path := cleanPath(reported); sourcepath.IsRepositoryRelative(path) {
			anchors = append(anchors, path)
		}
	}
	var files []FileEvidence
	var warnings []string
	seen := map[fileKey]bool{}
	warned := map[string]bool{}
	for _, reported := range editedFiles {
		path := cleanPath(reported)
		if !sourcepath.IsRepositoryRelative(path) {
			continue
		}
		anchors = append(anchors, path)
		key := fileKey{path: path, kind: session.TouchEdit}
		if seen[key] {
			continue
		}
		seen[key] = true
		files = append(files, FileEvidence{Path: path, Kind: session.TouchEdit})
	}
	for _, touch := range graph.FileTouches {
		if movedTouches[touch.ID] {
			continue
		}
		reported := cleanPath(touch.Path)
		path, ok := dirs.repoRelative(reported, touch.SessionID, anchors)
		if !ok {
			// One unrelativizable path can appear in dozens of touches and the
			// warnings land in a TEXT[] column, so each is reported once.
			if !warned[reported] {
				warned[reported] = true
				warnings = append(warnings, "cannot relativize path outside known cwd or repository root: "+reported)
			}
			continue
		}
		key := fileKey{path: path, kind: touch.Kind}
		if seen[key] {
			continue
		}
		seen[key] = true
		files = append(files, FileEvidence{Path: path, Kind: touch.Kind})
	}
	return files, warnings
}

// agentWorktree is the prefix an agent worktree inside the checkout adds to a
// path relativized against the main checkout. The pull request names the file
// without it.
var agentWorktree = regexp.MustCompile(`^(?:\.claude|\.agents|\.codex|\.cursor)/worktrees/[^/]+/|^\.worktrees/[^/]+/`)

// repoRelative rewrites one path reported in sessionID to be relative to the
// repository root. Recorded repository and worktree roots are tried first
// (the session's own, then the tree's; longest wins, so a worktree inside the
// checkout beats the checkout): a cwd can be a subdirectory, and cutting
// against it would drop the path's leading directories. Then the session's
// own cwd values, longest first, then the rest of the tree's (a nested
// session frequently records no cwd of its own but runs inside the tree that
// spawned it), then the anchor rule. A leading agent worktree is removed, and
// a cwd inside an in-repo agent worktree the tree worked in never cuts deeper
// than that worktree (outsideCwdInAgentWorktree). A path nothing relativizes is
// not evidence.
func (w workingDirs) repoRelative(path, sessionID string, anchors []string) (string, bool) {
	parsed := sourcepath.Parse(path)
	if !parsed.Valid() {
		return "", false
	}
	if parsed.IsRepositoryRelative() {
		return withoutAgentWorktree(parsed.String())
	}
	if !parsed.IsAbsolute() {
		return "", false
	}
	for i, dirs := range [][]string{w.rootsOf(sessionID), w.roots.values(), w.of(sessionID), w.pooled.values()} {
		if relative, ok := stripLongestDir(parsed.String(), dirs); ok {
			if i >= 2 {
				relative = w.outsideCwdInAgentWorktree(parsed.String(), relative)
			}
			return withoutAgentWorktree(relative)
		}
	}
	return anchoredSuffix(parsed.String(), anchors)
}

// inRepoWorktreeDir finds an agent worktree directory that tools create
// inside the checkout (<checkout>/{.claude,.agents}/worktrees/<name>).
// Cursor and Codex can keep theirs in
// the home directory (~/.cursor/worktrees/<repo>/<id>/,
// ~/.codex/worktrees/<id>/<repo>/), where the directory after the marker is
// not the checkout, so their markers, and .worktrees, are not used.
var inRepoWorktreeDir = regexp.MustCompile(`/(?:\.claude|\.agents)/worktrees/[^/]+`)

// outsideCwdInAgentWorktree re-cuts path at the in-repo agent worktree
// holding it when relative, its cut against a working directory, starts
// below that worktree: an agent that cds into platform/ of
// <checkout>/{.claude,.agents}/worktrees/<name> still edits platform/x.go,
// not x.go.
// The worktree counts only when the tree worked in it, so it is one of the
// tree's working directories or roots; when several nested worktrees are,
// the innermost one cuts. A worktree nested inside one the tree worked in is
// re-cut at the outer one, and the leading-worktree strip then removes the
// inner prefix, which gives the same repo-relative path. Directory names are
// compared exactly (drive letters are normalized first), so a Windows path
// that spells a directory in another case keeps the cwd cut.
func (w workingDirs) outsideCwdInAgentWorktree(path, relative string) string {
	matches := inRepoWorktreeDir.FindAllStringIndex(path, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		end := matches[i][1]
		if end >= len(path) || path[end] != '/' || !w.pooled.seen[path[:end]] {
			continue
		}
		if inWorktree := path[end+1:]; len(inWorktree) > len(relative) {
			return inWorktree
		}
		return relative
	}
	return relative
}

func withoutAgentWorktree(path string) (string, bool) {
	path = agentWorktree.ReplaceAllString(path, "")
	return path, path != ""
}

func stripLongestDir(path string, dirs []string) (string, bool) {
	bestDir, bestRelative := "", ""
	for _, dir := range dirs {
		relative, ok := sourcepath.Rel(dir, path)
		if !ok || len(dir) <= len(bestDir) {
			continue
		}
		bestDir, bestRelative = dir, relative
	}
	return bestRelative, bestDir != ""
}

// anchoredSuffix is the anchor rule used to resolve paths: an
// absolute tool path whose tail is the repo-relative spelling of a file the
// same tree already named is that file. It is the only signal that recovers a
// session which reported no working directory at all, and a path-boundary
// suffix match against a spelling the tree itself produced is evidence rather
// than a guess. Longest wins, so a tree holding both "x.go" and "web/x.go"
// resolves "/repo/web/x.go" to the latter.
func anchoredSuffix(path string, anchors []string) (string, bool) {
	best := ""
	for _, anchor := range anchors {
		if len(anchor) <= len(best) || !sourcepath.HasPathSuffix(path, anchor) {
			continue
		}
		best = anchor
	}
	return best, best != ""
}

func relativeTouchPaths(graph *session.Graph) []string {
	anchors := newStringSet()
	for _, touch := range graph.FileTouches {
		if path := cleanPath(touch.Path); sourcepath.IsRepositoryRelative(path) {
			if path, ok := withoutAgentWorktree(path); ok {
				anchors.add(path)
			}
		}
	}
	return anchors.values()
}

func cleanPath(value string) string { return sourcepath.Normalize(clean(value)) }

func commitRefs(graph *session.Graph, keys attributeKeys) []CommitRef {
	var refs commitRefSet
	addCreatedRefs(graph, &refs)
	addStartingRef(graph, keys, &refs)
	return refs.refs
}

// addCreatedRefs tags the SHAs a successful commit command printed. A command
// that looks like a commit is not enough — the output has to name a hash — so
// this reads the same evidence the mappers read to decide a session's outcome,
// without depending on them having decided it.
func addCreatedRefs(graph *session.Graph, refs *commitRefSet) {
	results := make(map[string]*session.ToolResult, len(graph.ToolResults))
	for i := range graph.ToolResults {
		results[graph.ToolResults[i].ToolUseID] = &graph.ToolResults[i]
	}
	for i := range graph.ToolUses {
		use := &graph.ToolUses[i]
		if use.Verb != session.VerbExecute || use.Status != session.StatusOK {
			continue
		}
		command := commandOf(use.Input)
		if command == "" || !gitevidence.IsCommitCommand(command) {
			continue
		}
		result, answered := results[use.ID]
		if !answered {
			continue
		}
		observedAt := result.Timestamp
		if observedAt == nil {
			observedAt = use.Timestamp
		}
		toolUseID := use.ID
		for _, sha := range gitevidence.CommitSHAs(resultText(result)) {
			refs.add(CommitRef{SHA: sha, Role: RoleCreated, ObservedAt: observedAt, ToolUseID: &toolUseID})
		}
	}
}

// addStartingRef reads the commit the working tree was on when the session
// started, which some vendors record in their session metadata. Only the root
// session's is taken: a nested session's starting commit describes that thread,
// not the tree the matcher is scoring.
func addStartingRef(graph *session.Graph, keys attributeKeys, refs *commitRefSet) {
	root := graph.Root()
	if root == nil || len(keys.startingCommit) == 0 {
		return
	}
	for _, attribute := range graph.Attributes {
		if attribute.SessionID != root.ID || !keys.startingCommit[attribute.Key] {
			continue
		}
		sha, ok := attributeString(attribute.Value)
		if !ok {
			continue
		}
		observedAt := root.StartedAt
		if observedAt == nil {
			validFrom := attribute.ValidFrom
			observedAt = &validFrom
		}
		refs.add(CommitRef{SHA: clean(sha), Role: RoleStarting, ObservedAt: observedAt})
	}
}

// resultText is everything a tool result said, as text. OutputText alone is not
// enough: Codex keeps a shell call's terminal output in the structured payload
// and only the response body in the text column, so a commit run there prints
// its hash into Output and nowhere else. The SHA pattern is a bracketed
// "[branch hash]", which survives JSON escaping intact, and only the results of
// calls already recognised as commit commands are read at all.
func resultText(result *session.ToolResult) string {
	var parts []string
	if result.OutputText != nil {
		parts = append(parts, *result.OutputText)
	}
	if len(result.Output) > 0 {
		parts = append(parts, string(result.Output))
	}
	return strings.Join(parts, "\n")
}

// commandOf pulls a shell command out of a tool input. The two observed
// spellings — Claude Code's {"command": "..."} and Codex's {"cmd": "..."} or
// {"command": ["...", ...]} — are read together rather than per vendor: they
// are unambiguous, and a tool shape carrying neither field yields "".
func commandOf(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var shape struct {
		Cmd     string          `json:"cmd"`
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(input, &shape) != nil {
		return ""
	}
	if shape.Cmd != "" {
		return shape.Cmd
	}
	if len(shape.Command) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(shape.Command, &text) == nil {
		return text
	}
	var parts []string
	if json.Unmarshal(shape.Command, &parts) == nil {
		if len(parts) >= 3 && isShellCommand(parts[0], parts[1]) {
			return parts[2]
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func isShellCommand(executable, flags string) bool {
	name := path.Base(executable)
	return (name == "bash" || name == "sh" || name == "zsh") &&
		strings.HasPrefix(flags, "-") && strings.Contains(strings.TrimLeft(flags, "-"), "c")
}

// attributeString decodes an attribute value that has to be a non-empty string
// to mean anything. JSON null — the attribute log's "unset from here on" — and
// every other shape report false.
func attributeString(value json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(value, &text) != nil || text == "" {
		return "", false
	}
	return text, true
}

// clean prepares a value captured from an agent for storage as text. A
// tool-reported path or branch can carry a NUL or invalid UTF-8 exactly like the
// rest of a transcript, and many text stores reject such values. clean returns
// valid text with those bytes removed or replaced, so one bad value cannot fail
// a whole ingest. Scrubbing happens on the way in so matching and deduplication
// see the stored spelling.
func clean(value string) string { return textclean.SafeText(value) }

type commitRefKey struct {
	sha  string
	role Role
}

// commitRefSet keeps the first observation of each (SHA, role) pair, with the
// SHA lowercased. the stored commit refs are keyed on
// (root session id, sha, role) and one commit legitimately shows up more than
// once — the same hash echoed by two successful commit commands — so a
// duplicate would collide on insert, and two spellings of one hash would be two
// rows saying the same thing.
//
// The case is normalised here because this is where the evidence is derived: a
// commit SHA from git is lowercase hex, so a lowercase ref lets the requeue
// prefix-match a stored SHA against the ref with an index instead of folding
// both sides per row (RequeueLinkageForSession).
type commitRefSet struct {
	seen map[commitRefKey]bool
	refs []CommitRef
}

func (s *commitRefSet) add(ref CommitRef) {
	if ref.SHA == "" {
		return
	}
	ref.SHA = strings.ToLower(ref.SHA)
	key := commitRefKey{sha: ref.SHA, role: ref.Role}
	if s.seen[key] {
		return
	}
	if s.seen == nil {
		s.seen = make(map[commitRefKey]bool)
	}
	s.seen[key] = true
	s.refs = append(s.refs, ref)
}

// stringSet is an insertion-ordered set, so one graph always correlates to one
// evidence ordering.
type stringSet struct {
	seen  map[string]bool
	order []string
}

func newStringSet() *stringSet { return &stringSet{seen: map[string]bool{}} }

func (s *stringSet) add(value string) {
	if value == "" || s.seen[value] {
		return
	}
	s.seen[value] = true
	s.order = append(s.order, value)
}

func (s *stringSet) values() []string { return s.order }
