package locallink

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

const (
	// maxReflogBytes bounds what is read of one reflog: the newest entries
	// of a larger file.
	maxReflogBytes = 8 << 20
	// maxReflogLine bounds one reflog line; a longer one is skipped.
	maxReflogLine = 64 << 10
	// reflogSlack is how far outside a tool call a reflog entry may fall: git
	// records whole seconds and the transcript's clock is not git's.
	reflogSlack = 2 * time.Second
)

// creationMessage matches the reflog messages of commits made in a working
// tree: commits, amends, cherry-picks, reverts, applied patches and the picks
// of a rebase (also those of "pull --rebase"). Merges are left out on purpose:
// the matcher excludes merge commits.
var creationMessage = regexp.MustCompile(`^(commit( \((amend|initial)\))?|cherry-pick|revert|am|[^:]*\((pick|continue|squash|fixup|edit|reword)\)): `)

// shellTools are the tools that run a command, or poll one that still runs.
var shellTools = map[string]bool{
	"bash": true, "exec_command": true, "shell": true, "shell_command": true,
	"local_shell": true, "exec": true, "write_stdin": true, "wait": true,
}

// gitWriteCommands are the git subcommands that can create a commit.
var gitWriteCommands = map[string]bool{"commit": true, "rebase": true, "cherry-pick": true, "revert": true, "am": true, "pull": true}

// reflogFile is one reflog of the clone. A HEAD log belongs to the working
// tree worktree, "" when that is unknown; a branch log belongs to none.
type reflogFile struct {
	path     string
	head     bool
	worktree string
	size     int64
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
	add := func(path string, head bool, worktree string) {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			files = append(files, reflogFile{path: path, head: head, worktree: worktree, size: info.Size()})
		}
	}
	add(filepath.Join(common, "logs", "HEAD"), true, mainRootOf(common))
	if entries, err := os.ReadDir(filepath.Join(common, "worktrees")); err == nil {
		for _, e := range entries {
			admin := filepath.Join(common, "worktrees", e.Name())
			recorded, err := os.ReadFile(filepath.Join(admin, "gitdir"))
			if err != nil {
				continue
			}
			gitdir := filepath.FromSlash(strings.TrimSpace(string(recorded)))
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(admin, gitdir)
			}
			add(filepath.Join(admin, "logs", "HEAD"), true, filepath.Dir(filepath.Clean(gitdir)))
		}
	}
	_ = filepath.WalkDir(filepath.Join(common, "logs", "refs", "heads"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			add(path, false, "")
		}
		return nil
	})
	return files
}

// creation is one reflog entry that records the making of a wanted commit.
type creation struct {
	sha      string
	at       time.Time
	head     bool
	worktree string
	kind     string
}

// reflog is what the clone's reflogs say about some commits.
type reflog struct {
	creations []creation
	// roots are the working trees that have a HEAD log.
	roots []string
	// mainRoot is the main working tree, "" when unknown.
	mainRoot string
}

// readReflog reads the creations of the wanted commits from the clone
// containing dir. Unreadable reflogs give no evidence.
func readReflog(ctx context.Context, dir string, wanted map[string]bool) reflog {
	common, err := gitlocal.CommonDir(ctx, dir)
	if err != nil {
		return reflog{}
	}
	files := reflogFiles(common)
	log := reflog{mainRoot: mainRootOf(common)}
	for _, f := range files {
		if f.head && f.worktree != "" {
			log.roots = append(log.roots, f.worktree)
		}
	}
	log.creations = readCreations(files, wanted)
	return log
}

// readCreations reads the creation entries of the wanted commits. A file over
// the size bound is read from its newest end, and a line that cannot be read
// is skipped.
func readCreations(files []reflogFile, wanted map[string]bool) []creation {
	var found []creation
	for _, f := range files {
		readLines(f, func(line string) {
			if c, ok := parseCreation(line, wanted); ok {
				c.head, c.worktree = f.head, f.worktree
				found = append(found, c)
			}
		})
	}
	return found
}

func readLines(f reflogFile, each func(string)) {
	file, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	skipFirst := false
	if f.size > maxReflogBytes {
		if _, err := file.Seek(f.size-maxReflogBytes, io.SeekStart); err != nil {
			return
		}
		skipFirst = true
	}
	r := bufio.NewReaderSize(file, maxReflogLine)
	for {
		line, err := r.ReadSlice('\n')
		long := false
		for err == bufio.ErrBufferFull {
			long = true
			_, err = r.ReadSlice('\n')
		}
		if skipFirst {
			skipFirst = false
		} else if !long && len(line) > 0 {
			each(strings.TrimRight(string(line), "\r\n"))
		}
		if err != nil {
			return
		}
	}
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
	kind, _, _ := strings.Cut(message, ": ")
	return creation{sha: fields[1], at: time.Unix(seconds, 0), kind: kind}, true
}

// ReflogFingerprint describes the creation entries the clone's reflogs hold
// for the commits, for a cache that must notice a new one. Moving HEAD or a
// branch without making one of the commits does not change it. It is empty
// when the reflogs cannot be listed.
func ReflogFingerprint(ctx context.Context, dir string, commits []string) string {
	wanted := make(map[string]bool, len(commits))
	for _, sha := range commits {
		wanted[sha] = true
	}
	log := readReflog(ctx, dir, wanted)
	lines := make([]string, 0, len(log.creations))
	for _, c := range log.creations {
		lines = append(lines, fmt.Sprintf("%s|%d|%t|%s|%s", c.sha, c.at.Unix(), c.head, c.worktree, c.kind))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, line := range lines {
		_, _ = fmt.Fprintf(h, "%s\x01", line)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// callWindow is the time a session's Git-writing shell call ran, and where.
type callWindow struct {
	session    string
	start, end time.Time
	root       string
}

// addReflogCommits supplies commit creation evidence from the clone's reflogs:
// a commit made while one session's Git-writing shell call ran in the same
// working tree was made by that session. It outranks hook observations, which
// guess the session from the process tree. Unreadable reflogs give no evidence.
func addReflogCommits(ctx context.Context, dir string, found []sessions.Session, commits []gitlocal.Commit) []sessions.Session {
	wanted := make(map[string]bool, len(commits))
	for _, c := range commits {
		wanted[c.OID] = true
	}
	log := readReflog(ctx, dir, wanted)
	if len(log.creations) == 0 {
		return found
	}
	locate := rootLocator(log.mainRoot, log.roots)
	var windows []callWindow
	for _, s := range found {
		windows = append(windows, callWindows(s, locate)...)
	}
	for sha, creator := range creatorsOf(log, windows) {
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

// creatorsOf names the session that made each commit, where exactly one
// Git-writing call fits.
//
// HEAD logs are authoritative: a commit one records is placed only by it, in
// the working tree it belongs to, and never falls back to branch logs (a
// HEAD log of unknown working tree places nothing). A commit no HEAD log
// records comes from a branch log, which knows no working tree, so only calls
// in working trees that no longer exist can match: a live working tree would
// have recorded the commit itself.
func creatorsOf(log reflog, windows []callWindow) map[string]string {
	live := map[string]bool{}
	for _, root := range log.roots {
		live[root] = true
	}
	bySHA := map[string][]creation{}
	for _, c := range log.creations {
		bySHA[c.sha] = append(bySHA[c.sha], c)
	}
	creators := map[string]string{}
	for sha, entries := range bySHA {
		inHead := slices.ContainsFunc(entries, func(c creation) bool { return c.head })
		candidates := map[string]bool{}
		for _, c := range entries {
			if c.head != inHead || (inHead && c.worktree == "") {
				continue
			}
			for _, w := range windows {
				if w.root == "" || c.at.Before(w.start.Add(-reflogSlack)) || c.at.After(w.end.Add(reflogSlack)) {
					continue
				}
				if (inHead && w.root == c.worktree) || (!inHead && !live[w.root]) {
					candidates[w.session] = true
				}
			}
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

// callWindows lists the Git-writing shell calls of one session, every nested
// session included, that finished with a timestamp. A call whose command
// does not write to Git, a poll of a running command among them, establishes
// nothing and has no window.
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
		command := commandText(traceprocessing.NormalizeCodexExecInput(use.Name, use.Input))
		if command == "" {
			continue
		}
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
		var dirs []string
		for _, named := range callDirs(use.Input, base) {
			for _, dir := range writeDirs(command, named) {
				if !slices.Contains(dirs, dir) {
					dirs = append(dirs, dir)
				}
			}
		}
		for _, dir := range dirs {
			windows = append(windows, callWindow{session: s.ID, start: *use.Timestamp, end: end, root: locate(dir)})
		}
	}
	return windows
}

// workdirInSource finds a workdir property of a code-mode program, whose key
// may be bare or quoted (escaped quotes included).
var workdirInSource = regexp.MustCompile(`(?:\\?["'\x60])?\bworkdir(?:\\?["'\x60])?\s*:\s*\\?["'\x60]([^"'\x60\\]+)`)

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

var commandSeparators = regexp.MustCompile(`&&|\|\||[\n;|&]`)

// writeDirs are the directories in which the command runs a Git subcommand
// that can create a commit, one for each such simple command. A "cd" and a
// "git -C" move the directory when they name a path; one that cannot be
// followed (a variable, "~") leaves it unknown and the call places nothing.
func writeDirs(command, cwd string) []string {
	var dirs []string
	for _, simple := range commandSeparators.Split(command, -1) {
		words := strings.Fields(simple)
		for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		switch name := filepath.Base(unquote(words[0])); name {
		case "cd":
			target := ""
			if len(words) > 1 {
				target = words[1]
			}
			cwd = moveTo(cwd, target)
		case "git":
			if dir, ok := gitWriteDir(words[1:], cwd); ok && dir != "" {
				dirs = append(dirs, dir)
			}
		case "bash", "sh", "zsh":
			if len(words) >= 3 && strings.HasPrefix(words[1], "-") && strings.Contains(words[1], "c") {
				dirs = append(dirs, writeDirs(unquote(strings.Join(words[2:], " ")), cwd)...)
			}
		}
	}
	return dirs
}

// gitWriteDir reads the arguments of a git command: it reports the directory
// it runs in and whether its subcommand can create a commit. The subcommand is
// the first word that is not an option or an option's value.
func gitWriteDir(args []string, cwd string) (string, bool) {
	dir := cwd
	for i := 0; i < len(args); i++ {
		arg := unquote(args[i])
		switch {
		case arg == "-C" || arg == "--work-tree":
			if i+1 < len(args) {
				dir = moveTo(dir, args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "--work-tree="):
			dir = moveTo(dir, strings.TrimPrefix(arg, "--work-tree="))
		case arg == "-c" || arg == "--git-dir" || arg == "--namespace" || arg == "--exec-path":
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			return dir, gitWriteCommands[arg]
		}
	}
	return dir, false
}

// moveTo is the directory after changing from dir to target: "" when it
// cannot be known.
func moveTo(dir, target string) string {
	target = unquote(target)
	switch {
	case target == "" || target == "-" || strings.ContainsAny(target[:1], "~$"):
		return ""
	case filepath.IsAbs(target):
		return filepath.Clean(target)
	case dir == "":
		return ""
	}
	return filepath.Join(dir, target)
}

func unquote(word string) string { return strings.Trim(word, `"'`) }

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
// {"command": "..."} and Codex's {"cmd": "..."} or {"command": [...]}; an
// argv that runs a shell script is that script.
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
		if len(parts) >= 3 && strings.HasPrefix(parts[1], "-") && strings.Contains(parts[1], "c") {
			return parts[2]
		}
		return strings.Join(parts, " ")
	}
	return ""
}
