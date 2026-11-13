package codex

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

type Entry struct {
	Path  string
	Meta  *codextranscript.SessionMeta
	Class codextranscript.ThreadClass
}

type ScanReport struct {
	SkippedFiles int      `json:"skipped_files"`
	SkippedPaths []string `json:"skipped_paths,omitempty"`
	// UnreadablePaths are the compressed rollouts (.jsonl.zst) whose zstd data
	// is corrupt or truncated. They are skipped too, and listed here as well.
	UnreadablePaths []string `json:"unreadable_paths,omitempty"`
}

type Files = model.Files

// RootFile is the root-only file list used when a damaged store index cannot
// resolve the rest of a tree.
func RootFile(path string) Files {
	return Files{{Path: path, Name: codextranscript.PlainName(filepath.Base(path)), Kind: model.KindTranscript}}
}

type candidate struct {
	entry   Entry
	cwd     string
	modTime time.Time
}

// scanned is one rollout's first-record read, kept per path so ScanStore can
// classify outcomes in sorted order after the concurrent reads finish.
type scanned struct {
	entry Entry
	err   error
}

// FindSessions scans the dated rollout store and returns repository-scoped
// root sessions with every child and fork source needed to decode each tree.
func FindSessions(sessionsDir string, roots []string) ([]model.Session, model.ScanReport, error) {
	return FindSessionsCached(sessionsDir, roots, nil)
}

// FindSessionsCached is FindSessions reading rollout first records through
// cache, which may be nil to read every one. The sessions found are the same
// either way.
func FindSessionsCached(sessionsDir string, roots []string, cache *Cache) ([]model.Session, model.ScanReport, error) {
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return nil, model.ScanReport{}, nil
	} else if err != nil {
		return nil, model.ScanReport{}, err
	}
	sessionsDir = filepath.Clean(sessionsDir)
	l := cache.index(sessionsDir)
	var sessions []model.Session
	for _, entry := range l.entries {
		c, ok := eligible(entry, sessionsDir, roots)
		if ok {
			sessions = append(sessions, buildSession(c, l.tree, l.warning))
		}
	}
	return sessions, l.report, nil
}

// loaded is one scan of the store: the entries, the tree lookups built from
// them once, and what the scan had to report.
type loaded struct {
	entries []Entry
	tree    *Tree
	report  model.ScanReport
	warning string
}

func loadIndex(sessionsDir string, cache *Cache) *loaded {
	l := &loaded{}
	l.entries, l.report, l.warning = loadEntries(sessionsDir, cache)
	l.tree = NewTree(l.entries)
	return l
}

func loadEntries(sessionsDir string, cache *Cache) ([]Entry, model.ScanReport, string) {
	index, scan, err := scanStore(filepath.Dir(sessionsDir), cache)
	if err == nil {
		var report model.ScanReport
		unreadable := map[string]bool{}
		for _, path := range scan.UnreadablePaths {
			unreadable[path] = true
		}
		for _, path := range scan.SkippedPaths {
			if unreadable[path] {
				report.Add(path, unreadableReason)
			} else {
				report.Add(path, "first record absent or incomplete")
			}
		}
		return index, report, ""
	}
	index, report := softIndex(sessionsDir, cache)
	return index, report, fmt.Sprintf("Codex store scan fell back to a partial index: %v", err)
}

func eligible(entry Entry, sessionsDir string, roots []string) (candidate, bool) {
	if entry.Class != codextranscript.ThreadRoot || entry.Meta == nil {
		return candidate{}, false
	}
	path := filepath.Clean(entry.Path)
	cwd := filepath.Clean(entry.Meta.CWD)
	if !model.PathWithin(sessionsDir, path) || entry.Meta.ID == "" || cwd == "" || !model.PathWithinAny(roots, cwd) || model.IsAgentRun(cwd) {
		return candidate{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return candidate{}, false
	}
	return candidate{entry: entry, cwd: cwd, modTime: info.ModTime().UTC()}, true
}

func buildSession(c candidate, tree *Tree, scanWarning string) model.Session {
	files := RootFile(c.entry.Path)
	var warnings []string
	if scanWarning != "" {
		warnings = append(warnings, scanWarning)
	}
	if found, err := tree.Find(c.entry); err != nil {
		warnings = append(warnings, fmt.Sprintf("Codex session tree discovery fell back to the root rollout: %v", err))
	} else {
		files = found
	}
	s := model.Session{
		Source: vendors.Codex, Path: c.entry.Path, SessionID: c.entry.Meta.ID,
		WorkingDir: c.cwd, ModifiedAt: c.modTime, Files: files, Warnings: warnings,
	}
	if c.entry.Meta.Timestamp != nil {
		s.StartedAt = c.entry.Meta.Timestamp.UTC()
	}
	return s
}

// ScanStore builds the first-line metadata index used by FIND. Rollout bodies
// are not read here.
func ScanStore(codexHome string) ([]Entry, ScanReport, error) {
	return scanStore(codexHome, nil)
}

// scanStore is ScanStore reading first records through cache, which may be nil.
func scanStore(codexHome string, cache *Cache) ([]Entry, ScanReport, error) {
	var report ScanReport
	var found []string
	scan := cache.beginScan(codexHome)
	for _, dir := range []string{filepath.Join(codexHome, "sessions"), filepath.Join(codexHome, "archived_sessions")} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if codextranscript.IsRolloutName(entry.Name()) {
				found = append(found, path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, report, fmt.Errorf("scanning %s: %w", dir, err)
		}
	}
	paths, siblings := chooseRollouts(found)
	slices.Sort(paths)
	// Reading each rollout's first record is independent per file and is what
	// scanning the store costs, so the reads run concurrently. Every outcome is
	// kept per path and interpreted below in sorted order, so which duplicate
	// thread id is reported, which paths are skipped, and which hard error wins
	// do not depend on the order the reads happened to finish.
	read := make([]scanned, len(paths))
	group := new(errgroup.Group)
	group.SetLimit(runtime.GOMAXPROCS(0))
	for i, path := range paths {
		group.Go(func() error {
			read[i].entry, read[i].err = scan.readOrSibling(path, siblings)
			return nil
		})
	}
	_ = group.Wait()
	scan.end()

	entries := make([]Entry, 0, len(paths))
	seen := map[string]string{}
	for i, path := range paths {
		entry, err := read[i].entry, read[i].err
		if errors.Is(err, codextranscript.ErrIncompleteFirstLine) || errors.Is(err, os.ErrNotExist) || errors.Is(err, errUnreadableCompressed) {
			report.SkippedFiles++
			report.SkippedPaths = append(report.SkippedPaths, path)
			if errors.Is(err, errUnreadableCompressed) {
				report.UnreadablePaths = append(report.UnreadablePaths, path)
			}
			continue
		}
		if err != nil {
			return nil, report, err
		}
		if other := seen[entry.Meta.ID]; other != "" {
			return nil, report, fmt.Errorf("rollouts %s and %s both report thread id %s", other, path, entry.Meta.ID)
		}
		seen[entry.Meta.ID] = path
		entries = append(entries, entry)
	}
	return entries, report, nil
}

// errUnreadableCompressed marks a compressed rollout whose zstd data cannot be
// decompressed. The rollout is skipped, not an error for the whole scan.
var errUnreadableCompressed = errors.New("compressed rollout is unreadable")

// unreadableReason is why a corrupt compressed rollout is skipped.
const unreadableReason = "compressed rollout is corrupt or truncated"

// chooseRollouts is the rollouts to read among the ones a walk found: a plain
// one, or a compressed one without a plain copy. siblings gives, for each
// plain rollout that has a compressed copy, that copy: Codex deletes the plain
// file once it has compressed it, which may happen after the walk.
func chooseRollouts(found []string) (paths []string, siblings map[string]string) {
	listed := make(map[string]bool, len(found))
	for _, path := range found {
		listed[path] = true
	}
	siblings = map[string]string{}
	for _, path := range found {
		switch {
		case !codextranscript.Compressed(path):
			paths = append(paths, path)
			if listed[path+".zst"] {
				siblings[path] = path + ".zst"
			}
		case !listed[codextranscript.PlainPath(path)]:
			paths = append(paths, path)
		}
	}
	return paths, siblings
}

// readOrSibling reads the rollout at path, or its compressed copy from
// siblings when the plain file is gone.
func (s *scan) readOrSibling(path string, siblings map[string]string) (Entry, error) {
	entry, err := s.read(path)
	if compressed, ok := siblings[path]; ok && errors.Is(err, os.ErrNotExist) {
		return s.read(compressed)
	}
	return entry, err
}

func readEntry(path string) (Entry, error) {
	handle, err := codextranscript.OpenRollout(path)
	if err != nil {
		return Entry{}, fmt.Errorf("opening rollout %s: %w", path, err)
	}
	defer handle.Close()
	line, err := codextranscript.DecodeFirst(handle)
	if err != nil {
		if errors.Is(err, codextranscript.ErrDecompress) {
			err = fmt.Errorf("%w: %w", errUnreadableCompressed, err)
		}
		return Entry{}, fmt.Errorf("reading rollout %s: %w", path, err)
	}
	class, err := codextranscript.ClassifyThread([]codextranscript.Line{line})
	if err != nil {
		return Entry{}, fmt.Errorf("classifying %s: %w", path, err)
	}
	return Entry{Path: path, Meta: line.SessionMeta, Class: class}, nil
}

// SessionWorkingDir returns the root session working directory from a rollout's
// first-line metadata. Child and internal rollouts are excluded.
func SessionWorkingDir(path string) (string, bool, error) {
	entry, err := readEntry(path)
	if err != nil {
		return "", false, err
	}
	if entry.Class != codextranscript.ThreadRoot || entry.Meta == nil || entry.Meta.CWD == "" {
		return "", false, nil
	}
	return filepath.Clean(entry.Meta.CWD), true, nil
}

// softIndex is the partial index of sessionsDir used when a full scan of the
// store fails. It reads first records through cache but never ends its scan:
// it does not walk archived_sessions, so ending it would evict the archived
// entries and the next full scan would read them all again.
func softIndex(sessionsDir string, cache *Cache) ([]Entry, model.ScanReport) {
	var found []string
	var index []Entry
	var report model.ScanReport
	scan := cache.beginScan(filepath.Dir(sessionsDir))
	_ = filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == sessionsDir {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() && codextranscript.IsRolloutName(d.Name()) {
			found = append(found, path)
		}
		return nil
	})
	paths, siblings := chooseRollouts(found)
	for _, path := range paths {
		entry, err := scan.readOrSibling(path, siblings)
		switch {
		case errors.Is(err, errUnreadableCompressed):
			report.Add(path, unreadableReason)
		case err != nil:
			report.Add(path, err.Error())
		default:
			index = append(index, entry)
		}
	}
	return index, report
}

// Find returns the file list for rootEntry using only the first-line index.
// Callers that look up several roots build one Tree and call its Find.
func Find(index []Entry, rootEntry Entry) (Files, error) {
	return NewTree(index).Find(rootEntry)
}

// Tree is the lookups Find needs over one scan of the store, built once so that
// finding each root session does not walk the whole index again.
type Tree struct {
	byID             map[string]*Entry
	childrenByParent map[string][]*Entry
	// buildErr is why no root can be found: an entry without thread metadata.
	buildErr error
	// chainErr is, per session id, the first parent chain error among the
	// session's children, in index order.
	chainErr map[string]error
}

// NewTree indexes entries by thread id and parent, and validates every parent
// chain.
func NewTree(index []Entry) *Tree {
	t := &Tree{
		byID:             make(map[string]*Entry, len(index)),
		childrenByParent: map[string][]*Entry{},
		chainErr:         map[string]error{},
	}
	for i := range index {
		entry := &index[i]
		if entry.Meta == nil || entry.Meta.ID == "" {
			t.buildErr = fmt.Errorf("store entry %s has no thread metadata", entry.Path)
			return t
		}
		t.byID[entry.Meta.ID] = entry
		if entry.Class == codextranscript.ThreadChild {
			t.childrenByParent[entry.Meta.ParentThreadID] = append(t.childrenByParent[entry.Meta.ParentThreadID], entry)
		}
	}
	for i := range index {
		child := &index[i]
		if child.Class != codextranscript.ThreadChild || t.chainErr[child.Meta.SessionID] != nil {
			continue
		}
		if _, err := walkToRoot(t.byID, child); err != nil {
			t.chainErr[child.Meta.SessionID] = err
		}
	}
	return t
}

// Find returns the file list for rootEntry.
func (t *Tree) Find(rootEntry Entry) (Files, error) {
	if t.buildErr != nil {
		return nil, t.buildErr
	}
	byID, childrenByParent := t.byID, t.childrenByParent
	if rootEntry.Meta == nil {
		return nil, fmt.Errorf("rollout %s is not in the store index", rootEntry.Path)
	}
	root := byID[rootEntry.Meta.ID]
	if root == nil {
		return nil, fmt.Errorf("rollout %s is not in the store index", rootEntry.Path)
	}
	if root.Class == codextranscript.ThreadInternal {
		return nil, fmt.Errorf("rollout %s is an internal thread and is excluded", root.Path)
	}
	if root.Class == codextranscript.ThreadChild {
		if real, err := walkToRoot(byID, root); err == nil {
			return nil, fmt.Errorf("rollout %s is a child; use root rollout %s", root.Path, real.Path)
		}
		return nil, fmt.Errorf("rollout %s is a child whose root is missing", root.Path)
	}
	if err := t.chainErr[root.Meta.SessionID]; err != nil {
		return nil, err
	}

	children := []*Entry{}
	type queued struct {
		entry *Entry
		depth int
	}
	queue := []queued{{entry: root}}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[parent.entry.Meta.ID] {
			if child.Meta.SessionID != root.Meta.SessionID {
				return nil, fmt.Errorf("child rollout %s reports session %s, want %s", child.Path, child.Meta.SessionID, root.Meta.SessionID)
			}
			depth := parent.depth + 1
			if spawn := child.Meta.Source.ThreadSpawn; spawn != nil && spawn.Depth != 0 && spawn.Depth != depth {
				return nil, fmt.Errorf("child rollout %s reports depth %d, want %d", child.Path, spawn.Depth, depth)
			}
			children = append(children, child)
			queue = append(queue, queued{entry: child, depth: depth})
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Meta.ID < children[j].Meta.ID })

	files := Files{{Path: root.Path, Name: codextranscript.PlainName(filepath.Base(root.Path)), Kind: model.KindTranscript}}
	seen := map[string]bool{filepath.Clean(root.Path): true}
	for _, child := range children {
		cleaned := filepath.Clean(child.Path)
		if !seen[cleaned] {
			files = append(files, model.SessionFile{Path: child.Path, Name: codextranscript.PlainName(filepath.Base(child.Path)), Kind: model.KindSubTranscript})
			seen[cleaned] = true
		}
		if child.Meta.SubagentHistoryStartOrdinal != nil || child.Meta.ForkedFromID == "" {
			continue
		}
		fork := byID[child.Meta.ForkedFromID]
		if fork == nil {
			return nil, fmt.Errorf("child rollout %s names missing fork source %s and has no subagent_history_start_ordinal", child.Path, child.Meta.ForkedFromID)
		}
		if cleaned := filepath.Clean(fork.Path); !seen[cleaned] {
			files = append(files, model.SessionFile{Path: fork.Path, Name: codextranscript.PlainName(filepath.Base(fork.Path)), Kind: model.KindForkSource})
			seen[cleaned] = true
		}
	}
	sort.Slice(files[1:], func(i, j int) bool { return files[i+1].Name < files[j+1].Name })
	return files, nil
}

func walkToRoot(byID map[string]*Entry, start *Entry) (*Entry, error) {
	seen := map[string]bool{}
	entry := start
	for entry.Class == codextranscript.ThreadChild {
		if seen[entry.Meta.ID] {
			return nil, fmt.Errorf("child rollout %s is part of a parent cycle", start.Path)
		}
		seen[entry.Meta.ID] = true
		spawn := entry.Meta.Source.ThreadSpawn
		if spawn == nil || spawn.ParentThreadID != entry.Meta.ParentThreadID {
			return nil, fmt.Errorf("child rollout %s has disagreeing parent links", entry.Path)
		}
		parent := byID[entry.Meta.ParentThreadID]
		if parent == nil {
			return nil, fmt.Errorf("child rollout %s names missing parent %s", entry.Path, entry.Meta.ParentThreadID)
		}
		entry = parent
	}
	if entry.Class != codextranscript.ThreadRoot {
		return nil, fmt.Errorf("child rollout %s has no root parent", start.Path)
	}
	return entry, nil
}
