// Package clonefind finds the local clones of GitHub repositories without
// asking: in the folders coding agents worked in and below the home
// directory. It reads local files and runs read-only git commands; it makes no
// network request, and nothing it learns leaves the computer.
package clonefind

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/scanskip"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/collector/codex"
	"github.com/baum-ai/baum/sessions/collector/cursor"
	"github.com/baum-ai/baum/sessions/collector/model"
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
	DefaultMaxDepth     = 5
	DefaultMaxProbes    = 2000
	DefaultMaxWalked    = 50000
	DefaultProbeTimeout = 3 * time.Second
	// maxStores bounds how many session stores are probed, newest first.
	maxStores = 3000
)

// systemTempDirs are the temporary directories of Unix systems besides
// os.TempDir: an agent's scratch work there is nobody's clone.
var systemTempDirs = []string{"/tmp", "/var/tmp", "/var/folders", "/private/tmp", "/private/var/tmp", "/private/var/folders"}

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
	// ExtraRoots are further folders to walk besides the home directory.
	ExtraRoots []string
	// TempDirs replaces the temporary directories (os.TempDir and the usual
	// Unix ones), for tests. A folder an agent worked in below one of them is
	// never a candidate.
	TempDirs []string
	// GOOS is the operating system, runtime.GOOS by default. On darwin
	// ~/Documents is a protected folder whose first read prompts the user, so
	// it is never read; elsewhere it is walked like the rest of the home
	// directory.
	GOOS string
	Now  func() time.Time
	// Probe replaces the git probe, for tests.
	Probe Probe
	// MaxDepth is how deep a walk goes below its folder (default
	// DefaultMaxDepth), MaxProbes how many directories are probed with git
	// (DefaultMaxProbes), MaxWalked how many directories a run walks in all
	// folders together (DefaultMaxWalked) and ProbeTimeout the time each git command gets
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
	if o.TempDirs == nil {
		o.TempDirs = []string{os.TempDir()}
		if o.GOOS != "windows" {
			o.TempDirs = append(o.TempDirs, systemTempDirs...)
		}
	}
	if o.Probe == nil {
		timeout := o.ProbeTimeout
		o.Probe = func(ctx context.Context, dir string) (gitlocal.Light, error) {
			return gitlocal.Probe(ctx, dir, timeout)
		}
	}
	if o.Stores.ClaudeProjectsDir == "" && len(o.Stores.ClaudeProjectsDirs) == 0 {
		o.Stores.ClaudeProjectsDirs = agentdirs.Claude.Under(o.Home, "projects")
	}
	if o.Stores.CodexSessionsDir == "" && len(o.Stores.CodexSessionsDirs) == 0 {
		o.Stores.CodexSessionsDirs = agentdirs.Codex.Under(o.Home, "sessions")
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
	// walked counts the directories walked in this run.
	walked      int
	walkedRoots []string
	// temps are the temporary directories, as given and resolved.
	temps []string
	byDir map[string]*workTree
	byTop map[string]*workTree
}

func newFinder(o Options) *finder {
	o = o.withDefaults()
	home := canonical(o.Home)
	f := &finder{o: o, home: home, byDir: map[string]*workTree{}, byTop: map[string]*workTree{}}
	for _, dir := range o.TempDirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		f.temps = append(f.temps, filepath.Clean(dir))
		if resolved := canonical(dir); resolved != "" {
			f.temps = append(f.temps, resolved)
		}
	}
	return f
}

// Find locates local clones from agent activity and below the home directory.
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
			// The main clone stands for its linked worktrees.
			f.mainOf(ctx, tree)
		}
	}

	// The home directory, then any further folders.
	f.walkedRoots = append(f.walkedRoots, f.home)
	f.walk(ctx, f.home, true)
	for _, root := range f.o.ExtraRoots {
		if ctx.Err() != nil {
			break
		}
		root, ok := f.walkable(root)
		if !ok || f.insideWalked(root) {
			continue
		}
		f.walkedRoots = append(f.walkedRoots, root)
		f.walk(ctx, root, false)
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
		// A directory baum made to run a review agent in is nobody's clone,
		// whatever repository lies above it, and neither is an agent's
		// temporary work.
		if model.IsAgentRun(dir) || f.temporary(dir) {
			return
		}
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

// insideWalked reports a folder an earlier walk already covered.
func (f *finder) insideWalked(root string) bool {
	for _, walked := range f.walkedRoots {
		if rel, err := filepath.Rel(walked, root); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
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

// resolve maps a directory an agent worked in to its working tree, or nil when
// the directory is gone, is not in a clone of a GitHub repository, or is the
// root, the home directory or above it.
func (f *finder) resolve(ctx context.Context, dir string) *workTree {
	// Protected folders are dropped before anything touches them.
	if !filepath.IsAbs(dir) || f.protected(filepath.Clean(dir)) {
		return nil
	}
	dir = canonical(dir)
	if dir == "" || f.protected(dir) || f.temporary(dir) {
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
	// A repository in a temporary folder, a linked worktree's included, is
	// an agent's scratch work.
	if top == "" || common == "" || f.unsafe(top) || f.temporary(top) || f.temporary(common) {
		return nil
	}
	// A working tree set elsewhere by core.worktree is not this directory.
	if rel, err := filepath.Rel(top, canonical(dir)); err != nil || strings.HasPrefix(rel, "..") {
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

// protected reports a folder macOS guards with a privacy prompt: reading it
// asks the user, so nothing is read in it.
func (f *finder) protected(path string) bool {
	if f.o.GOOS != "darwin" {
		return false
	}
	for _, home := range []string{f.home, filepath.Clean(f.o.Home)} {
		rel, err := filepath.Rel(home, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		switch {
		case strings.EqualFold(parts[0], "Documents"), strings.EqualFold(parts[0], "Desktop"), strings.EqualFold(parts[0], "Downloads"):
			return true
		case strings.EqualFold(parts[0], "Library") && len(parts) > 1 &&
			(strings.EqualFold(parts[1], "Mobile Documents") || strings.EqualFold(parts[1], "CloudStorage")):
			return true
		}
	}
	return false
}

// mainOf probes the main clone of a linked worktree, so that it stands for the
// worktree: agents work in worktrees kept in hidden folders of the clone or of
// the home directory, which no walk would find. It returns nil unless
// the repository directory is <clone>/.git of a non-bare working tree at a
// place a scan may read.
func (f *finder) mainOf(ctx context.Context, tree *workTree) *workTree {
	if filepath.Base(tree.common) != ".git" {
		return nil
	}
	main := filepath.Dir(tree.common)
	if main == tree.top || f.unsafe(main) || f.protected(main) || f.temporary(main) {
		return nil
	}
	if _, ok := f.walkable(main); !ok {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(main, ".git")); err != nil {
		return nil
	}
	found := f.probe(ctx, main)
	if found == nil || found.top != main || found.common != tree.common {
		return nil
	}
	return found
}

// documentsAllowed reports whether ~/Documents may be walked: everywhere but
// on macOS, where reading it asks the user.
func (f *finder) documentsAllowed() bool { return f.o.GOOS != "darwin" }

// personal reports a folder of the home directory a walk never enters:
// personal files and cloud mounts (scanskip.UserDataFor), which on macOS
// include every folder whose first read asks the user, and Downloads
// everywhere.
func (f *finder) personal(name string) bool {
	if name == "Documents" && f.documentsAllowed() {
		return false
	}
	return scanskip.UserDataFor(f.o.GOOS, name)
}

// temporary reports a folder an agent's work there is never a clone in: one
// below a temporary directory, below an agent's scratchpad, or in Downloads.
func (f *finder) temporary(path string) bool {
	fold := f.o.GOOS == "darwin" || f.o.GOOS == "windows"
	below := func(dir string) bool {
		if fold {
			dir, path := strings.ToLower(dir), strings.ToLower(path)
			rel, err := filepath.Rel(dir, path)
			return err == nil && !strings.HasPrefix(rel, "..")
		}
		rel, err := filepath.Rel(dir, path)
		return err == nil && !strings.HasPrefix(rel, "..")
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if strings.EqualFold(part, "scratchpad") {
			return true
		}
	}
	for _, dir := range f.temps {
		if below(dir) {
			return true
		}
	}
	for _, home := range []string{f.home, filepath.Clean(f.o.Home)} {
		if home != "" && home != "." && below(filepath.Join(home, "Downloads")) {
			return true
		}
	}
	return false
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
			if scanskip.UserDataFor(f.o.GOOS, part) || scanskip.DirFor(f.o.GOOS, part) {
				return "", false
			}
		}
	}
	return root, true
}

// walk finds the working trees below root, breadth first so the shallow ones
// come before the budget runs out, to MaxDepth, without following symlinks
// and without entering what scanskip lists. It stops at a directory with a
// .git entry: a repository is not searched for others. In the home directory
// (home set) it neither stops at the home's own .git (a dotfiles repository)
// nor enters its personal folders.
func (f *finder) walk(ctx context.Context, root string, home bool) {
	type dir struct {
		path  string
		depth int
	}
	queue := []dir{{root, 0}}
	for len(queue) > 0 {
		if ctx.Err() != nil {
			return
		}
		d := queue[0]
		queue = queue[1:]
		if f.walked++; f.walked > f.o.MaxWalked {
			return
		}
		if !home || d.depth > 0 {
			if _, err := os.Lstat(filepath.Join(d.path, ".git")); err == nil {
				f.probe(ctx, d.path)
				continue
			}
		}
		if d.depth >= f.o.MaxDepth {
			continue
		}
		entries, err := os.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			// A symlink is not a directory entry here, so none is followed.
			name := entry.Name()
			if !entry.IsDir() || scanskip.DirFor(f.o.GOOS, name) || (home && d.depth == 0 && f.personal(name)) {
				continue
			}
			if f.walked+len(queue) >= f.o.MaxWalked {
				break
			}
			queue = append(queue, dir{filepath.Join(d.path, name), d.depth + 1})
		}
	}
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
	main := f.mainOf(ctx, tree)
	var matches []Match
	for _, name := range tree.names {
		merged := map[string][]Candidate{}
		add(merged, name, tree.candidate())
		if main != nil {
			add(merged, name, main.candidate())
		}
		matches = append(matches, Match{Repository: name, Candidate: merged[name][0]})
	}
	return matches
}
