package locallink

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

const (
	// maxReflogBytes skips a reflog file larger than this, and maxReflogLines
	// bounds the lines read from one.
	maxReflogBytes = 8 << 20
	maxReflogLines = 100000
	// reflogSlack is how far outside a tool call a reflog entry may fall: git
	// records whole seconds and the transcript's clock is not git's.
	reflogSlack = 2 * time.Second
)

// creationMessage matches the reflog messages of commits made in a working
// tree. Merges are left out on purpose: the matcher excludes merge commits.
var creationMessage = regexp.MustCompile(`^(commit( \((amend|initial)\))?|cherry-pick|revert|rebase( -i)? \((pick|continue|squash|fixup|edit|reword)\)): `)

// writesGit matches a command that can create a commit.
var writesGit = regexp.MustCompile(`\bgit\b[^\n;&|]*\b(commit|rebase|cherry-pick|revert|am)\b`)

// shellTools are the tools that run a command, or poll one that still runs.
var shellTools = map[string]bool{
	"bash": true, "exec_command": true, "shell": true, "shell_command": true,
	"local_shell": true, "exec": true, "write_stdin": true, "wait": true,
}

// reflogFile is one reflog of the clone. worktree is the working tree a HEAD
// log belongs to; a branch log has none.
type reflogFile struct {
	path     string
	worktree string
	size     int64
	modTime  time.Time
}

// mainRootOf is the main working tree of a clone whose git directory sits
// inside it, and "" for a bare or separated one.
func mainRootOf(common string) string {
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return ""
}

// reflogFiles lists the clone's reflogs: the main working tree's HEAD log,
// each linked worktree's, and every branch log, which outlives its worktree.
func reflogFiles(common string) []reflogFile {
	var files []reflogFile
	add := func(path, worktree string) {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			files = append(files, reflogFile{path: path, worktree: worktree, size: info.Size(), modTime: info.ModTime()})
		}
	}
	add(filepath.Join(common, "logs", "HEAD"), mainRootOf(common))
	if entries, err := os.ReadDir(filepath.Join(common, "worktrees")); err == nil {
		for _, e := range entries {
			admin := filepath.Join(common, "worktrees", e.Name())
			recorded, err := os.ReadFile(filepath.Join(admin, "gitdir"))
			if err != nil {
				continue
			}
			root := filepath.Dir(filepath.Clean(strings.TrimSpace(string(recorded))))
			add(filepath.Join(admin, "logs", "HEAD"), root)
		}
	}
	heads := filepath.Join(common, "logs", "refs", "heads")
	_ = filepath.WalkDir(heads, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			add(path, "")
		}
		return nil
	})
	return files
}

// ReflogFingerprint describes the state of the reflogs of the clone containing
// dir, for a cache that must notice a new commit. It is empty when they cannot
// be listed. Only file metadata is read.
func ReflogFingerprint(ctx context.Context, dir string) string {
	common, err := gitlocal.CommonDir(ctx, dir)
	if err != nil {
		return ""
	}
	h := sha256.New()
	for _, f := range reflogFiles(common) {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x01", f.path, f.size, f.modTime.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// creation is one reflog entry that records the making of a wanted commit.
type creation struct {
	sha      string
	at       time.Time
	worktree string
}

func readCreations(files []reflogFile, wanted map[string]bool) []creation {
	var found []creation
	for _, f := range files {
		if f.size > maxReflogBytes {
			continue
		}
		file, err := os.Open(f.path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(nil, 1<<20)
		for lines := 0; scanner.Scan() && lines < maxReflogLines; lines++ {
			if c, ok := parseCreation(scanner.Text(), wanted); ok {
				c.worktree = f.worktree
				found = append(found, c)
			}
		}
		_ = file.Close()
	}
	return found
}

// parseCreation reads "<old> <new> <name> <<email>> <seconds> <zone>\t<message>".
func parseCreation(line string, wanted map[string]bool) (creation, bool) {
	header, message, ok := strings.Cut(line, "\t")
	if !ok || !creationMessage.MatchString(message) {
		return creation{}, false
	}
	fields := strings.Fields(header)
	if len(fields) < 5 || len(fields[0]) != len(fields[1]) || !wanted[fields[1]] {
		return creation{}, false
	}
	seconds, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
	if err != nil {
		return creation{}, false
	}
	return creation{sha: fields[1], at: time.Unix(seconds, 0)}, true
}

// callWindow is the time a session's shell tool call ran, and where.
type callWindow struct {
	session   string
	start     time.Time
	end       time.Time
	root      string
	writesGit bool
}

// addReflogCommits supplies commit creation evidence from the clone's reflogs:
// a commit made while one session's shell call ran in the same working tree was
// made by that session. It outranks hook observations, which guess the session
// from the process tree. Unreadable reflogs give no evidence.
func addReflogCommits(ctx context.Context, dir string, found []sessions.Session, commits []gitlocal.Commit) []sessions.Session {
	common, err := gitlocal.CommonDir(ctx, dir)
	if err != nil {
		return found
	}
	wanted := make(map[string]bool, len(commits))
	for _, c := range commits {
		wanted[c.OID] = true
	}
	files := reflogFiles(common)
	creations := readCreations(files, wanted)
	if len(creations) == 0 {
		return found
	}
	var roots []string
	for _, f := range files {
		if f.worktree != "" {
			roots = append(roots, f.worktree)
		}
	}
	locate := rootLocator(mainRootOf(common), roots)

	var windows []callWindow
	for _, s := range found {
		windows = append(windows, callWindows(s, locate)...)
	}
	for sha, creator := range creatorsOf(creations, windows) {
		applied := false
		for i := range found {
			if found[i].ID == creator && found[i].Vendor != "" {
				if !slices.Contains(found[i].CreatedCommits, sha) {
					found[i].CreatedCommits = append(slices.Clone(found[i].CreatedCommits), sha)
				}
				applied = true
			}
		}
		if !applied {
			continue
		}
		for i := range found {
			if found[i].ID != creator && slices.Contains(found[i].CreatedCommits, sha) {
				found[i].CreatedCommits = slices.DeleteFunc(slices.Clone(found[i].CreatedCommits), func(c string) bool { return c == sha })
			}
		}
	}
	return found
}

// creatorsOf names the session that made each commit, where exactly one did.
func creatorsOf(creations []creation, windows []callWindow) map[string]string {
	type match struct{ scoped, any, writing map[string]bool }
	matches := map[string]*match{}
	for _, c := range creations {
		m := matches[c.sha]
		if m == nil {
			m = &match{scoped: map[string]bool{}, any: map[string]bool{}, writing: map[string]bool{}}
			matches[c.sha] = m
		}
		for _, w := range windows {
			if w.root == "" || (c.worktree != "" && w.root != c.worktree) ||
				c.at.Before(w.start.Add(-reflogSlack)) || c.at.After(w.end.Add(reflogSlack)) {
				continue
			}
			if c.worktree != "" {
				m.scoped[w.session] = true
			}
			m.any[w.session] = true
			if w.writesGit {
				m.writing[w.session] = true
			}
		}
	}
	creators := map[string]string{}
	for sha, m := range matches {
		candidates := m.any
		if len(m.scoped) > 0 {
			candidates = m.scoped
		}
		if len(candidates) > 1 {
			narrowed := map[string]bool{}
			for id := range candidates {
				if m.writing[id] {
					narrowed[id] = true
				}
			}
			candidates = narrowed
		}
		if len(candidates) == 1 {
			for id := range candidates {
				creators[sha] = id
			}
		}
	}
	return creators
}

// rootLocator maps a directory to the root of the working tree holding it, or
// "" outside the clone. A directory under a conventional worktrees folder of
// the main working tree belongs to that worktree even once it was removed.
func rootLocator(mainRoot string, roots []string) func(string) string {
	var conventional []string
	if mainRoot != "" {
		for _, folder := range []string{".claude", ".agents"} {
			conventional = append(conventional, filepath.Join(mainRoot, folder, "worktrees"))
		}
		roots = append(slices.Clone(roots), mainRoot)
	}
	return func(path string) string {
		path = filepath.Clean(path)
		for _, folder := range conventional {
			if rel, ok := within(folder, path); ok && rel != "." {
				name, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
				return filepath.Join(folder, name)
			}
		}
		best := ""
		for _, root := range roots {
			if _, ok := within(root, path); ok && len(root) > len(best) {
				best = root
			}
		}
		return best
	}
}

// within reports path relative to root when path equals or lies under it.
func within(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// callWindows lists the shell calls of one session, every nested session
// included, that finished with a timestamp.
func callWindows(s sessions.Session, locate func(string) string) []callWindow {
	g := s.Graph
	if g == nil {
		return nil
	}
	results := make(map[string]time.Time, len(g.ToolResults))
	for _, r := range g.ToolResults {
		if r.Timestamp != nil {
			results[r.ToolUseID] = *r.Timestamp
		}
	}
	workingDirs := map[string]string{}
	for _, nested := range g.Sessions {
		if nested.WorkingDir != nil {
			workingDirs[nested.ID] = *nested.WorkingDir
		}
	}
	top := ""
	if root := s.Root(); root != nil {
		top = root.ID
	}
	var windows []callWindow
	for _, use := range g.ToolUses {
		end, ok := results[use.ID]
		if use.Timestamp == nil || !ok || !shellTools[strings.ToLower(use.Name)] {
			continue
		}
		input := traceprocessing.NormalizeCodexExecInput(use.Name, use.Input)
		command := commandText(input)
		base := cwdAt(g, use.SessionID, top, *use.Timestamp)
		if base == "" {
			base = workingDirs[use.SessionID]
		}
		if base == "" {
			base = workingDirs[top]
		}
		if base == "" {
			base = s.WorktreeRoot
		}
		for _, dir := range callDirs(use.Input, base) {
			windows = append(windows, callWindow{
				session: s.ID, start: *use.Timestamp, end: end, root: locate(dir), writesGit: writesGit.MatchString(command),
			})
		}
	}
	return windows
}

var workdirInSource = regexp.MustCompile("\\bworkdir\\s*:\\s*[\"'`]([^\"'`]+)[\"'`]")

// callDirs are the directories a call ran in: its own workdir fields when it
// names any, else base.
func callDirs(input json.RawMessage, base string) []string {
	var named []string
	var shape struct {
		Workdir string `json:"workdir"`
		Input   string `json:"input"`
	}
	var source string
	if json.Unmarshal(input, &shape) == nil {
		named = append(named, shape.Workdir)
		source = shape.Input
	} else {
		_ = json.Unmarshal(input, &source)
	}
	for _, m := range workdirInSource.FindAllStringSubmatch(source, -1) {
		named = append(named, m[1])
	}
	var dirs []string
	for _, dir := range named {
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		if dir = filepath.Clean(dir); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 && base != "" {
		dirs = append(dirs, base)
	}
	return dirs
}

// cwdAt is the "cwd" attribute in effect at time at, for the nested session
// that made the call and else the top-level session.
func cwdAt(g *session.Graph, nested, top string, at time.Time) string {
	for _, id := range []string{nested, top} {
		var latest time.Time
		cwd := ""
		for _, a := range g.Attributes {
			if a.SessionID != id || a.Key != "cwd" || a.ValidFrom.After(at) || a.ValidFrom.Before(latest) {
				continue
			}
			var value string
			if json.Unmarshal(a.Value, &value) != nil {
				value = ""
			}
			latest, cwd = a.ValidFrom, value
		}
		if cwd != "" {
			return cwd
		}
	}
	return ""
}

// commandText reads the command of a shell tool input: Claude Code's
// {"command": "..."} and Codex's {"cmd": "..."} or {"command": [...]}.
func commandText(input json.RawMessage) string {
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
	var text string
	if json.Unmarshal(shape.Command, &text) == nil {
		return text
	}
	var parts []string
	if json.Unmarshal(shape.Command, &parts) == nil {
		return strings.Join(parts, " ")
	}
	return ""
}
