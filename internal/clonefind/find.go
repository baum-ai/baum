// Package clonefind finds the local clones of GitHub repositories without
// asking: in the folders coding agents worked in and in the usual code
// folders. It reads local files and runs read-only git commands; it makes no
// network request, and nothing it learns leaves the computer.
package clonefind

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/scanskip"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/collector/codex"
	"github.com/baum-ai/baum/sessions/collector/cursor"
	"github.com/baum-ai/baum/sessions/vendors"
)

// Where a candidate was found.
const (
	// SourceAgent is a folder a coding agent worked in, from a hook event or
	// a session store.
	SourceAgent = "agent"
	// SourceScan is a clone found only by walking a code folder.
	SourceScan = "scan"
)

// Default limits; see Options.
const (
	DefaultMaxDepth     = 4
	DefaultMaxProbes    = 2000
	DefaultMaxWalked    = 50000
	DefaultProbeTimeout = 3 * time.Second
	// maxStores bounds how many session stores are probed, newest first.
	maxStores = 3000
)

// DefaultFolders are the code folders, relative to the home directory, that a
// scan walks when they exist.
var DefaultFolders = []string{"code", "src", "dev", "projects", "workspace", "repos", "git", filepath.Join("go", "src", "github.com")}

// Candidate is one local clone of a repository.
type Candidate struct {
	// Path is the working tree's top-level directory, and CommonDir the
	// repository directory its linked worktrees share. Both have symlinks
	// resolved, as the registry stores them.
	Path      string
	CommonDir string
	// LastActivity is when an agent last worked in the clone (or one of its
	// worktrees); zero for a clone only a scan found.
	LastActivity time.Time
	Source       string
}

// Result is what one run found.
type Result struct {
	ScannedAt time.Time
	// Repositories maps a lowercase owner/name to its clones, best first.
	Repositories map[string][]Candidate
}

// Events is the record of hook events.
type Events interface {
	Recent(limit int) ([]hookevents.Record, error)
}

// Probe resolves a directory to its working tree and remotes.
type Probe func(ctx context.Context, dir string) (gitlocal.Light, error)

// Options configure Find. Only Home is required.
type Options struct {
	// Home is the user's home directory.
	Home string
	// Events is the hook event record; nil reads none.
	Events Events
	// Stores locates the agent session stores. Empty directories default to
	// the usual ones below Home.
	Stores collector.StoreOptions
	// Folders replaces DefaultFolders (relative to Home, or absolute).
	Folders []string
	// ExtraRoots are further folders to walk.
	ExtraRoots []string
	// GOOS is the operating system, runtime.GOOS by default. On darwin
	// ~/Documents is a protected folder whose first read prompts the user, so
	// it is walked only when IncludeDocuments is set; elsewhere it is walked
	// like the other code folders.
	GOOS             string
	IncludeDocuments bool
	Now              func() time.Time
	// Probe replaces the git probe, for tests.
	Probe Probe
	// MaxDepth is how deep a walk goes below its folder (default
	// DefaultMaxDepth), MaxProbes how many directories are probed with git
	// (DefaultMaxProbes), MaxWalked how many directories a run walks
	// (DefaultMaxWalked) and ProbeTimeout the time each git command gets
	// (DefaultProbeTimeout).
	MaxDepth, MaxProbes, MaxWalked int
	ProbeTimeout                   time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxProbes <= 0 {
		o.MaxProbes = DefaultMaxProbes
	}
	if o.MaxWalked <= 0 {
		o.MaxWalked = DefaultMaxWalked
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = DefaultProbeTimeout
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Folders == nil {
		o.Folders = DefaultFolders
	}
	if o.Probe == nil {
		timeout := o.ProbeTimeout
		o.Probe = func(ctx context.Context, dir string) (gitlocal.Light, error) {
			return gitlocal.Probe(ctx, dir, timeout)
		}
	}
	if o.Stores.ClaudeProjectsDir == "" {
		o.Stores.ClaudeProjectsDir = filepath.Join(o.Home, ".claude", "projects")
	}
	if o.Stores.CodexSessionsDir == "" {
		o.Stores.CodexSessionsDir = filepath.Join(o.Home, ".codex", "sessions")
	}
	return o
}

// workTree is a probed working tree.
type workTree struct {
	top, common string
	// names are the GitHub repositories its remotes name, the one the
	// registry would use first.
	names    []string
	activity time.Time
	agent    bool
}

type finder struct {
	o      Options
	home   string
	probes int
	byDir  map[string]*workTree
	byTop  map[string]*workTree
}

func newFinder(o Options) *finder {
	o = o.withDefaults()
	home := canonical(o.Home)
	return &finder{o: o, home: home, byDir: map[string]*workTree{}, byTop: map[string]*workTree{}}
}

// Find locates local clones from agent activity and the code folders.
func Find(ctx context.Context, o Options) (Result, error) {
	f := newFinder(o)
	result := Result{ScannedAt: f.o.Now().UTC(), Repositories: map[string][]Candidate{}}
	if f.home == "" {
		return result, nil
	}

	// Agent folders: hook events and session stores.
	for _, dir := range f.agentDirs(ctx) {
		if ctx.Err() != nil {
			break
		}
		if tree := f.resolve(ctx, dir.path); tree != nil {
			tree.agent = true
			if dir.at.After(tree.activity) {
				tree.activity = dir.at
			}
		}
	}

	// Code folders, and the folders agent clones sit in.
	roots := f.scanRoots()
	var parents []string
	for _, tree := range f.agentTrees() {
		if parent := filepath.Dir(tree.top); f.inHome(parent) {
			parents = append(parents, parent)
		}
	}
	sort.Strings(parents)
	seen := map[string]bool{}
	for _, root := range append(roots, parents...) {
		if ctx.Err() != nil {
			break
		}
		root, ok := f.walkable(root)
		if !ok || seen[root] {
			continue
		}
		seen[root] = true
		f.walk(ctx, root)
	}

	group(&result, f.byTop)
	return result, ctx.Err()
}

type agentDir struct {
	path string
	at   time.Time
}

// agentDirs lists the working directories agents recorded, most recent first.
func (f *finder) agentDirs(ctx context.Context) []agentDir {
	latest := map[string]time.Time{}
	note := func(dir string, at time.Time) {
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		dir = filepath.Clean(dir)
		if at.After(latest[dir]) || latest[dir].IsZero() {
			latest[dir] = at
		}
	}
	if f.o.Events != nil {
		if records, err := f.o.Events.Recent(0); err == nil {
			for _, record := range records {
				if dir, ok := AgentDir(record.Event); ok {
					at := record.Event.Time
					if at.IsZero() {
						at = record.Received
					}
					note(dir, at)
				}
			}
		}
	}
	if stores, err := collector.FindStores(f.o.Stores); err == nil {
		// Stores are oldest first; keep the newest.
		if len(stores) > maxStores {
			stores = stores[len(stores)-maxStores:]
		}
		for _, store := range stores {
			if ctx.Err() != nil {
				break
			}
			switch store.Source {
			case vendors.ClaudeCode:
				if dir, err := claudecode.SessionWorkingDir(store.Path); err == nil {
					note(dir, store.ModifiedAt)
				}
			case vendors.Codex:
				if dir, root, err := codex.SessionWorkingDir(store.Path); err == nil && root {
					note(dir, store.ModifiedAt)
				}
			case vendors.Cursor:
				if found, err := cursor.SessionWorkingDirs(store.Path); err == nil {
					for _, session := range found {
						note(session.WorkingDir, session.ModifiedAt)
					}
				}
			}
		}
	}
	dirs := make([]agentDir, 0, len(latest))
	for dir, at := range latest {
		dirs = append(dirs, agentDir{dir, at})
	}
	sort.Slice(dirs, func(i, j int) bool {
		if !dirs[i].at.Equal(dirs[j].at) {
			return dirs[i].at.After(dirs[j].at)
		}
		return dirs[i].path < dirs[j].path
	})
	return dirs
}

// AgentDir is the directory an agent hook event worked in: the agent's own
// working directory, else the hook's.
func AgentDir(e hookevents.Event) (string, bool) {
	if e.Kind != hookevents.KindAgent {
		return "", false
	}
	dir := e.Dir
	if e.Session != nil && e.Session.Cwd != "" {
		dir = e.Session.Cwd
	}
	return dir, dir != ""
}

func (f *finder) agentTrees() []*workTree {
	var trees []*workTree
	for _, tree := range f.byTop {
		if tree.agent {
			trees = append(trees, tree)
		}
	}
	sort.Slice(trees, func(i, j int) bool { return trees[i].top < trees[j].top })
	return trees
}

// unsafe reports a directory no clone or scan may be: the filesystem root,
// the home directory, or anything above it.
func (f *finder) unsafe(dir string) bool {
	if filepath.Dir(dir) == dir || dir == f.home {
		return true
	}
	rel, err := filepath.Rel(dir, f.home)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// inHome reports a directory below the home directory.
func (f *finder) inHome(dir string) bool {
	rel, err := filepath.Rel(f.home, dir)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// resolve maps a directory an agent worked in to its working tree, or nil when
// the directory is gone, is not in a clone of a GitHub repository, or is the
// root, the home directory or above it.
func (f *finder) resolve(ctx context.Context, dir string) *workTree {
	dir = canonical(dir)
	if dir == "" {
		return nil
	}
	if info, err := os.Stat(dir); err != nil {
		return nil
	} else if !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	if tree, ok := f.byDir[dir]; ok {
		return tree
	}
	// Cheap search for the working tree before spending a git command.
	top := ""
	for d := dir; !f.unsafe(d); d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			top = d
			break
		}
	}
	if top == "" {
		f.byDir[dir] = nil
		return nil
	}
	tree := f.probe(ctx, top)
	f.byDir[dir] = tree
	return tree
}

// probe runs git in a directory that has a .git entry.
func (f *finder) probe(ctx context.Context, dir string) *workTree {
	if tree, ok := f.byDir[dir]; ok {
		return tree
	}
	f.byDir[dir] = nil
	if f.probes >= f.o.MaxProbes || ctx.Err() != nil {
		return nil
	}
	f.probes++
	light, err := f.o.Probe(ctx, dir)
	if err != nil {
		return nil
	}
	top, common := canonical(light.Top), canonical(light.CommonDir)
	if top == "" || common == "" || f.unsafe(top) {
		return nil
	}
	if tree, ok := f.byTop[top]; ok {
		f.byDir[dir] = tree
		return tree
	}
	names := githubRepositories(light.Remotes)
	if len(names) == 0 {
		return nil
	}
	tree := &workTree{top: top, common: common, names: names}
	f.byTop[top] = tree
	f.byDir[dir] = tree
	return tree
}

// githubRepositories lists the github.com repositories the remotes name,
// origin first as the registry reads them, then the others by remote name.
func githubRepositories(remotes []gitlocal.Remote) []string {
	sorted := append([]gitlocal.Remote(nil), remotes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Name == "origin", sorted[j].Name == "origin"
		if a != b {
			return a
		}
		return sorted[i].Name < sorted[j].Name
	})
	var names []string
	for _, remote := range sorted {
		host, name, ok := gitlocal.RemoteRepository(remote.URL)
		if !ok || (host != "github.com" && host != "www.github.com" && host != "ssh.github.com") {
			continue
		}
		if !contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// scanRoots are the code folders that exist, resolved.
func (f *finder) scanRoots() []string {
	var roots []string
	folders := append([]string(nil), f.o.Folders...)
	if f.documentsAllowed() {
		folders = append(folders, "Documents")
	}
	for _, folder := range append(folders, f.o.ExtraRoots...) {
		if !filepath.IsAbs(folder) {
			folder = filepath.Join(f.home, folder)
		}
		roots = append(roots, folder)
	}
	return roots
}

// documentsAllowed reports whether ~/Documents may be walked.
func (f *finder) documentsAllowed() bool {
	return f.o.GOOS != "darwin" || f.o.IncludeDocuments
}

// walkable resolves a folder to scan and refuses the filesystem root, the home
// directory, what is above it, and a folder of personal files below it, also
// when a symlink leads there.
func (f *finder) walkable(root string) (string, bool) {
	root = canonical(root)
	if root == "" || f.unsafe(root) {
		return "", false
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", false
	}
	if rel, err := filepath.Rel(f.home, root); err == nil && !strings.HasPrefix(rel, "..") {
		for i, part := range strings.Split(rel, string(filepath.Separator)) {
			if i == 0 && part == "Documents" && f.documentsAllowed() {
				continue
			}
			if scanskip.UserData(part) || scanskip.Dir(part) {
				return "", false
			}
		}
	}
	return root, true
}

// walk finds the working trees below root, to MaxDepth, without following
// symlinks and without entering what scanskip lists.
func (f *finder) walk(ctx context.Context, root string) {
	walked := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walked++; walked > f.o.MaxWalked {
			return filepath.SkipAll
		}
		if path != root {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return filepath.SkipDir
			}
			depth := strings.Count(rel, string(filepath.Separator)) + 1
			if depth > f.o.MaxDepth || scanskip.Dir(entry.Name()) {
				return filepath.SkipDir
			}
		}
		if _, statErr := os.Lstat(filepath.Join(path, ".git")); statErr != nil {
			return nil
		}
		f.probe(ctx, path)
		return filepath.SkipDir
	})
}

// canonical resolves symlinks, as the registry stores paths; "" when the path
// does not exist.
func canonical(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	return filepath.Clean(resolved)
}

// group files every probed working tree under each repository it names, one
// candidate per repository directory (linked worktrees collapse into one),
// best first.
func group(result *Result, trees map[string]*workTree) {
	for _, tree := range trees {
		for _, name := range tree.names {
			add(result.Repositories, name, tree.candidate())
		}
	}
	for name := range result.Repositories {
		sortCandidates(result.Repositories[name])
	}
}

func (t *workTree) candidate() Candidate {
	c := Candidate{Path: t.top, CommonDir: t.common, LastActivity: t.activity, Source: SourceScan}
	if t.agent {
		c.Source = SourceAgent
	}
	return c
}

// add files c under name, merging it into a candidate of the same repository
// directory: the main working tree stands for its linked worktrees, else the
// most recently used one.
func add(into map[string][]Candidate, name string, c Candidate) {
	list := into[name]
	for i, other := range list {
		if other.CommonDir != c.CommonDir {
			continue
		}
		keep, drop := other, c
		if preferred(c, other) {
			keep, drop = c, other
		}
		if drop.LastActivity.After(keep.LastActivity) {
			keep.LastActivity = drop.LastActivity
		}
		if drop.Source == SourceAgent {
			keep.Source = SourceAgent
		}
		list[i] = keep
		return
	}
	into[name] = append(list, c)
}

// preferred reports whether a should stand for its repository directory
// rather than b.
func preferred(a, b Candidate) bool {
	aMain := a.CommonDir == filepath.Join(a.Path, ".git")
	bMain := b.CommonDir == filepath.Join(b.Path, ".git")
	if aMain != bMain {
		return aMain
	}
	if !a.LastActivity.Equal(b.LastActivity) {
		return a.LastActivity.After(b.LastActivity)
	}
	return a.Path < b.Path
}

// sortCandidates puts clones an agent worked in first, most recent first, then
// the others by path.
func sortCandidates(list []Candidate) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if (a.Source == SourceAgent) != (b.Source == SourceAgent) {
			return a.Source == SourceAgent
		}
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		return a.Path < b.Path
	})
}

// Match is a clone found for one repository.
type Match struct {
	Repository string
	Candidate  Candidate
}

// Locate resolves one directory an agent worked in to the repositories its
// clone's remotes name, or nothing when it is not in such a clone.
func Locate(ctx context.Context, o Options, dir string, at time.Time) []Match {
	f := newFinder(o)
	if f.home == "" {
		return nil
	}
	tree := f.resolve(ctx, dir)
	if tree == nil {
		return nil
	}
	tree.agent, tree.activity = true, at
	var matches []Match
	for _, name := range tree.names {
		matches = append(matches, Match{Repository: name, Candidate: tree.candidate()})
	}
	return matches
}
