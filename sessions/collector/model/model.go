package model

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lastpersonlabs/baum/sessions/vendors"
)

// FileKind says what a session file is to the source that produced it.
type FileKind string

const (
	// KindTranscript is the session's own transcript: the Claude Code main
	// thread, or the Codex root rollout. Exactly one file per session has it.
	KindTranscript FileKind = "transcript"
	// KindSubTranscript is the transcript of a thread nested under the root: a
	// Claude subagent, or a Codex collaboration child.
	KindSubTranscript FileKind = "sub_transcript"
	// KindSubMetadata is a metadata file describing a nested thread rather than a
	// transcript of it: Claude's `agent-<A>.meta.json`, the only durable record
	// of the spawn tree.
	KindSubMetadata FileKind = "sub_metadata"
	// KindForkSource is a transcript that a nested thread forked from.
	KindForkSource FileKind = "fork_source"
)

// SessionFile is one file needed to reconstruct a session tree.
type SessionFile struct {
	// Path is where the file lives on this machine.
	Path string `json:"path"`
	// Data holds generated file content when the source cannot safely upload
	// its backing store. Exactly one of Path and Data is set.
	Data []byte `json:"-"`
	// Name is the name the source gave the file. It is content rather than
	// layout — Claude encodes the session id in `<S>.jsonl` and the agent id in
	// `agent-<A>.jsonl` — so it travels with the file into the archive.
	Name string `json:"name"`
	// Kind is what the file is to its source.
	Kind FileKind `json:"kind"`
}

// Files adapts found session files to transcript decoders.
type Files []SessionFile

func (f Files) Len() int { return len(f) }
func (f Files) At(i int) (string, string, string) {
	return f[i].Name, f[i].Path, string(f[i].Kind)
}

// Session is one local session tree whose work happened inside a repository.
type Session struct {
	Source     vendors.Slug `json:"source"`
	Path       string       `json:"path"`
	SessionID  string       `json:"session_id"`
	WorkingDir string       `json:"working_dir"`
	// RepoRoot is the checkout the session belongs to, and WorktreeRoot the git
	// worktree it actually ran in when that is a deeper directory. Both are
	// resolved while the checkout still exists; either may be empty.
	RepoRoot     string    `json:"repo_root,omitempty"`
	WorktreeRoot string    `json:"worktree_root,omitempty"`
	ModifiedAt   time.Time `json:"modified_at"`
	// Files names the whole tree, the root transcript first.
	Files    []SessionFile `json:"files"`
	Warnings []string      `json:"warnings,omitempty"`
}

// SkippedFile is one session file discovery found but could not read.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ScanReport is what a discovery pass dropped along the way.
type ScanReport struct {
	Skipped []SkippedFile `json:"skipped,omitempty"`
}

// Add records one unreadable file.
func (r *ScanReport) Add(path, reason string) {
	r.Skipped = append(r.Skipped, SkippedFile{Path: path, Reason: reason})
}

// Merge folds another pass's report into this one.
func (r *ScanReport) Merge(other ScanReport) {
	r.Skipped = append(r.Skipped, other.Skipped...)
}

// PathWithin reports whether path is root or a path inside it. Both arguments
// must be cleaned absolute paths. The match ends at a path separator, so
// /repo-other is not within /repo, and a file system root contains nothing
// but itself. On Windows, where the file system ignores
// case and agents record a drive letter in either case, case is ignored too.
func PathWithin(root, path string) bool {
	return pathWithin(root, path, FoldCase)
}

// FoldCase is whether paths on this platform compare without regard to case.
const FoldCase = runtime.GOOS == "windows"

func pathWithin(root, path string, fold bool) bool {
	if len(path) < len(root) || !SameText(path[:len(root)], root, fold) {
		return false
	}
	// A root ending in a separator is a volume root such as / or C:\, which
	// would contain every session on the machine; it matches only itself.
	if len(path) == len(root) {
		return true
	}
	return !strings.HasSuffix(root, string(filepath.Separator)) && path[len(root)] == filepath.Separator
}

// SameText compares two path spellings, ignoring case when fold is set.
func SameText(a, b string, fold bool) bool {
	if fold {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// PathWithinAny reports whether path lies within any of the roots.
func PathWithinAny(roots []string, path string) bool {
	for _, root := range roots {
		if PathWithin(root, path) {
			return true
		}
	}
	return false
}
