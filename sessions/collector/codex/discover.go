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
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/baumai/baum/sessions/codextranscript"
	"github.com/baumai/baum/sessions/collector/model"
	"github.com/baumai/baum/sessions/vendors"
)

type Entry struct {
	Path  string
	Meta  *codextranscript.SessionMeta
	Class codextranscript.ThreadClass
}

type ScanReport struct {
	SkippedFiles int      `json:"skipped_files"`
	SkippedPaths []string `json:"skipped_paths,omitempty"`
}

type Files = model.Files

// RootFile is the root-only file list used when a damaged store index cannot
// resolve the rest of a tree.
func RootFile(path string) Files {
	return Files{{Path: path, Name: filepath.Base(path), Kind: model.KindTranscript}}
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
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return nil, model.ScanReport{}, nil
	} else if err != nil {
		return nil, model.ScanReport{}, err
	}
	sessionsDir = filepath.Clean(sessionsDir)
	index, report, scanWarning := loadIndex(sessionsDir)
	var sessions []model.Session
	for _, entry := range index {
		c, ok := eligible(entry, sessionsDir, roots)
		if ok {
			sessions = append(sessions, buildSession(c, index, scanWarning))
		}
	}
	return sessions, report, nil
}

func loadIndex(sessionsDir string) ([]Entry, model.ScanReport, string) {
	index, scan, err := ScanStore(filepath.Dir(sessionsDir))
	if err == nil {
		var report model.ScanReport
		for _, path := range scan.SkippedPaths {
			report.Add(path, "first record absent or incomplete")
		}
		return index, report, ""
	}
	index, report := softIndex(sessionsDir)
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

func buildSession(c candidate, index []Entry, scanWarning string) model.Session {
	files := RootFile(c.entry.Path)
	var warnings []string
	if scanWarning != "" {
		warnings = append(warnings, scanWarning)
	}
	if found, err := Find(index, c.entry); err != nil {
		warnings = append(warnings, fmt.Sprintf("Codex session tree discovery fell back to the root rollout: %v", err))
	} else {
		files = found
	}
	return model.Session{
		Source: vendors.Codex, Path: c.entry.Path, SessionID: c.entry.Meta.ID,
		WorkingDir: c.cwd, ModifiedAt: c.modTime, Files: files, Warnings: warnings,
	}
}

// ScanStore builds the first-line metadata index used by FIND. Rollout bodies
// are not read here.
func ScanStore(codexHome string) ([]Entry, ScanReport, error) {
	var report ScanReport
	var paths []string
	for _, dir := range []string{filepath.Join(codexHome, "sessions"), filepath.Join(codexHome, "archived_sessions")} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			switch {
			case strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl"):
				paths = append(paths, path)
			case strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl.zst"):
				if _, statErr := os.Stat(strings.TrimSuffix(path, ".zst")); statErr != nil {
					return fmt.Errorf("compressed rollout %s has no plain twin; .zst decoding is unsupported", path)
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, report, fmt.Errorf("scanning %s: %w", dir, err)
		}
	}
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
			read[i].entry, read[i].err = readEntry(path)
			return nil
		})
	}
	_ = group.Wait()

	entries := make([]Entry, 0, len(paths))
	seen := map[string]string{}
	for i, path := range paths {
		entry, err := read[i].entry, read[i].err
		if errors.Is(err, codextranscript.ErrIncompleteFirstLine) || errors.Is(err, os.ErrNotExist) {
			report.SkippedFiles++
			report.SkippedPaths = append(report.SkippedPaths, path)
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

func readEntry(path string) (Entry, error) {
	handle, err := os.Open(path)
	if err != nil {
		return Entry{}, fmt.Errorf("opening rollout %s: %w", path, err)
	}
	defer handle.Close()
	line, err := codextranscript.DecodeFirst(handle)
	if err != nil {
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

func softIndex(sessionsDir string) ([]Entry, model.ScanReport) {
	var index []Entry
	var report model.ScanReport
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
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || filepath.Ext(d.Name()) != ".jsonl" {
			return nil
		}
		entry, err := readEntry(path)
		if err != nil {
			report.Add(path, err.Error())
		} else {
			index = append(index, entry)
		}
		return nil
	})
	return index, report
}

// Find returns the file list for rootEntry using only the first-line index.
func Find(index []Entry, rootEntry Entry) (Files, error) {
	byID := make(map[string]*Entry, len(index))
	childrenByParent := map[string][]*Entry{}
	for i := range index {
		entry := &index[i]
		if entry.Meta == nil || entry.Meta.ID == "" {
			return nil, fmt.Errorf("store entry %s has no thread metadata", entry.Path)
		}
		byID[entry.Meta.ID] = entry
		if entry.Class == codextranscript.ThreadChild {
			childrenByParent[entry.Meta.ParentThreadID] = append(childrenByParent[entry.Meta.ParentThreadID], entry)
		}
	}
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
	if err := validateParentChains(byID, root.Meta.SessionID); err != nil {
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

	files := Files{{Path: root.Path, Name: filepath.Base(root.Path), Kind: model.KindTranscript}}
	seen := map[string]bool{filepath.Clean(root.Path): true}
	for _, child := range children {
		cleaned := filepath.Clean(child.Path)
		if !seen[cleaned] {
			files = append(files, model.SessionFile{Path: child.Path, Name: filepath.Base(child.Path), Kind: model.KindSubTranscript})
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
			files = append(files, model.SessionFile{Path: fork.Path, Name: filepath.Base(fork.Path), Kind: model.KindForkSource})
			seen[cleaned] = true
		}
	}
	sort.Slice(files[1:], func(i, j int) bool { return files[i+1].Name < files[j+1].Name })
	return files, nil
}

func validateParentChains(byID map[string]*Entry, sessionID string) error {
	for _, child := range byID {
		if child.Class == codextranscript.ThreadChild && child.Meta.SessionID == sessionID {
			if _, err := walkToRoot(byID, child); err != nil {
				return err
			}
		}
	}
	return nil
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
