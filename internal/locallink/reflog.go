package locallink

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/agentcmd"
	"github.com/baum-ai/agentcmd/model"
	nativestd "github.com/baum-ai/agentcmd/native/standard"
	"github.com/baum-ai/agentcmd/rules/standard"

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
// tree: commits (also the one that finishes a cherry-pick), amends,
// cherry-picks, reverts, applied patches and the picks of a rebase (also those
// of "pull --rebase"). Merges are left out on purpose: the matcher excludes
// merge commits.
var creationMessage = regexp.MustCompile(`^(commit( \((amend|initial|cherry-pick)\))?|cherry-pick|revert|am|[^:]*\((pick|continue|squash|fixup|edit|reword)\)): `)

// gitWriteCommands are the git subcommands that can create a commit.
var gitWriteCommands = map[string]bool{"commit": true, "rebase": true, "cherry-pick": true, "revert": true, "am": true, "pull": true}

// reflogFile is one reflog of the clone. A HEAD log belongs to the working
// tree worktree, "" when that is unknown; a branch log belongs to the branch.
type reflogFile struct {
	path     string
	head     bool
	worktree string
	branch   string
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
	add := func(f reflogFile) {
		if info, err := os.Lstat(f.path); err == nil && info.Mode().IsRegular() {
			f.size = info.Size()
			files = append(files, f)
		}
	}
	add(reflogFile{path: filepath.Join(common, "logs", "HEAD"), head: true, worktree: realPath(mainRootOf(common))})
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
			add(reflogFile{path: filepath.Join(admin, "logs", "HEAD"), head: true, worktree: realPath(filepath.Dir(filepath.Clean(gitdir)))})
		}
	}
	heads := filepath.Join(common, "logs", "refs", "heads")
	_ = filepath.WalkDir(heads, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if rel, err := filepath.Rel(heads, p); err == nil {
				add(reflogFile{path: p, branch: filepath.ToSlash(rel)})
			}
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
	branch   string
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
// containing dir. Unreadable reflogs give no evidence. repo, when not nil, is
// the clone already open, whose repository directory needs no git process.
func readReflog(ctx context.Context, repo *gitlocal.Repo, dir string, wanted map[string]bool) reflog {
	var common string
	if repo != nil {
		common = repo.CommonDir()
	} else {
		var err error
		if common, err = gitlocal.CommonDir(ctx, dir); err != nil {
			return reflog{}
		}
	}
	files := reflogFiles(common)
	log := reflog{mainRoot: realPath(mainRootOf(common))}
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
				c.head, c.worktree, c.branch = f.head, f.worktree, f.branch
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
// A fast-forward ("cherry-pick --ff") makes no commit: it only moves a ref to
// one that exists.
func parseCreation(line string, wanted map[string]bool) (creation, bool) {
	header, message, ok := strings.Cut(line, "\t")
	if !ok || !creationMessage.MatchString(message) || strings.HasSuffix(strings.TrimSpace(message), ": fast-forward") {
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
// branch without making one of the commits does not change it. A clone whose
// reflogs cannot be read fingerprints as having no entries. repo, when not
// nil, is the clone of dir already open.
func ReflogFingerprint(ctx context.Context, repo *gitlocal.Repo, dir string, commits []string) string {
	wanted := make(map[string]bool, len(commits))
	for _, sha := range commits {
		wanted[sha] = true
	}
	log := readReflog(ctx, repo, dir, wanted)
	lines := make([]string, 0, len(log.creations))
	for _, c := range log.creations {
		lines = append(lines, fmt.Sprintf("%s|%d|%t|%s|%s|%s", c.sha, c.at.Unix(), c.head, c.worktree, c.branch, c.kind))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, line := range lines {
		_, _ = fmt.Fprintf(h, "%s\x01", line)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// callWindow is the time a session's successful Git-writing shell call ran,
// where, and on which branch (empty when unknown).
type callWindow struct {
	session    string
	start, end time.Time
	root       string
	branch     string
}

// addReflogCommits supplies commit creation evidence from the clone's reflogs:
// a commit made while one session's successful Git-writing shell call ran in
// the same working tree was made by that session. It outranks hook
// observations, which guess the session from the process tree when the agent
// exports no session id. Unreadable
// reflogs give no evidence. A session the load left unmapped takes its calls
// from its digest in book; with a nil book every session must be mapped.
func addReflogCommits(ctx context.Context, repo *gitlocal.Repo, dir string, found []sessions.Session, commits []gitlocal.Commit, book *digestBook) []sessions.Session {
	wanted := make(map[string]bool, len(commits))
	for _, c := range commits {
		wanted[c.OID] = true
	}
	log := readReflog(ctx, repo, dir, wanted)
	if len(log.creations) == 0 {
		return found
	}
	locate := rootLocator(log.mainRoot, log.roots)
	var windows []callWindow
	for _, s := range found {
		windows = append(windows, callWindows(s.ID, book.gitWrites(ctx, s), locate)...)
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
// A commit object is made once, so only a commit's earliest creation entries
// count (those within its first second, such as a HEAD log and a branch log
// of one event); a later entry is a ref moving to an existing commit.
//
// HEAD logs are authoritative: a commit one records is placed only by a call
// in the working tree it belongs to, and never falls back to branch logs (a
// HEAD log of unknown working tree places nothing). A commit no HEAD log
// records comes from a branch log, which knows no working tree, so only a call
// in a working tree that no longer exists and on that branch can match: a live
// working tree would have recorded the commit itself.
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
		earliest := entries[0].at
		for _, c := range entries {
			if c.at.Before(earliest) {
				earliest = c.at
			}
		}
		entries = slices.DeleteFunc(slices.Clone(entries), func(c creation) bool { return c.at.After(earliest) })
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
				if (inHead && w.root == c.worktree) || (!inHead && !live[w.root] && w.branch != "" && w.branch == c.branch) {
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
// "" outside the clone. The deepest linked working tree that exists wins;
// otherwise a directory under a conventional worktrees folder of the main
// working tree belongs to that worktree even once it was removed.
func rootLocator(mainRoot string, roots []string) func(string) string {
	var linked, conventional []string
	for _, root := range roots {
		if root != mainRoot {
			linked = append(linked, root)
		}
	}
	if mainRoot != "" {
		for _, folder := range []string{".claude", ".agents"} {
			conventional = append(conventional, filepath.Join(mainRoot, folder, "worktrees"))
		}
	}
	return func(p string) string {
		p = filepath.Clean(p)
		best := ""
		for _, root := range linked {
			if _, ok := within(root, p); ok && len(root) > len(best) {
				best = root
			}
		}
		if best != "" {
			return best
		}
		for _, folder := range conventional {
			if rel, ok := within(folder, p); ok && rel != "." {
				name, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
				return filepath.Join(folder, name)
			}
		}
		if mainRoot != "" {
			if _, ok := within(mainRoot, p); ok {
				return mainRoot
			}
		}
		return ""
	}
}

// within reports path relative to root when path equals or lies under it.
func within(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// callOutcome is what a tool call's result says.
type callOutcome struct {
	at     time.Time
	status session.ToolStatus
	text   string
}

// processID finds the id a shell tool reports for a command that is still
// running: `"session_id":N` in a structured result, `session ID N` in the text
// of a native one.
var processID = regexp.MustCompile(`"session_id"\s*:\s*(\d+)|session ID (\d+)`)

// polledProcess finds the id a poll names in its input: native JSON, or the
// `write_stdin({session_id:N` call of a code-mode program.
var polledProcess = regexp.MustCompile(`write_stdin\s*\(\s*\{[^}]*?\bsession_id["']?\s*:\s*(\d+)`)

func processIDs(text string) []string {
	var ids []string
	for _, m := range processID.FindAllStringSubmatch(text, -1) {
		id := m[1] + m[2]
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// poll is a successful call that looked at commands still running.
type poll struct {
	ids        []string
	start, end time.Time
	output     string
}

// pollsOf lists the polls of a graph by start time: native write_stdin and
// wait calls and code-mode programs that call write_stdin.
func pollsOf(g *session.Graph, results map[string]callOutcome) []poll {
	var polls []poll
	for _, use := range g.ToolUses {
		result, ok := results[use.ID]
		if use.Verb != session.VerbExecute || use.Timestamp == nil || !ok || result.status != session.StatusOK {
			continue
		}
		var ids []string
		switch strings.ToLower(use.Name) {
		case "write_stdin", "wait":
			var shape struct {
				SessionID json.Number `json:"session_id"`
			}
			if json.Unmarshal(use.Input, &shape) == nil && shape.SessionID != "" {
				ids = []string{shape.SessionID.String()}
			}
		case traceprocessing.CodexExecName:
			if source, ok := codeModeSource(use.Input); ok {
				for _, m := range polledProcess.FindAllStringSubmatch(source, -1) {
					ids = append(ids, m[1])
				}
			}
		}
		if len(ids) > 0 {
			polls = append(polls, poll{ids: ids, start: *use.Timestamp, end: result.at, output: result.text})
		}
	}
	slices.SortFunc(polls, func(a, b poll) int { return a.start.Compare(b.start) })
	return polls
}

// windowEnd is when the call's command was last known to be running. A command
// that yielded while it ran (a commit waiting on slow hooks) ends in a later
// poll of the same process, so the window reaches to the last poll that still
// found it running, or the one that found it finished. failed is set when
// that poll reports a non-zero exit: the command did not succeed.
func windowEnd(start, end time.Time, text string, polls []poll) (time.Time, bool) {
	active := processIDs(text)
	for _, p := range polls {
		if len(active) == 0 {
			break
		}
		if p.start.Before(start) || !slices.ContainsFunc(p.ids, func(id string) bool { return slices.Contains(active, id) }) {
			continue
		}
		if p.end.After(end) {
			end = p.end
		}
		still := processIDs(p.output)
		finished := slices.DeleteFunc(slices.Clone(active), func(id string) bool { return !slices.Contains(p.ids, id) || slices.Contains(still, id) })
		if len(finished) > 0 && reportsFailure(p.output) {
			return end, true
		}
		active = slices.DeleteFunc(active, func(id string) bool { return slices.Contains(finished, id) })
	}
	return end, false
}

// gitWrite is a successful shell call of a session that runs a git command
// that can create a commit: when it ran (through its last poll), the
// directories of those commands, and the branch it ran on ("" when unknown).
// It depends on the session's graph alone, so a session digest keeps it.
type gitWrite struct {
	start, end time.Time
	dirs       []string
	branch     string
}

// callWindows places the Git-writing calls of one session in the clone's
// working trees. A directory inside a nested repository is left out. Each
// directory is compared in its real form, as the reflog's working trees are.
func callWindows(session string, writes []gitWrite, locate func(string) string) []callWindow {
	var windows []callWindow
	for _, w := range writes {
		for _, dir := range w.dirs {
			dir = realPath(dir)
			root := locate(dir)
			if nestedRepository(dir, root) {
				continue
			}
			windows = append(windows, callWindow{session: session, start: w.start, end: w.end, root: root, branch: w.branch})
		}
	}
	return windows
}

// gitWrites lists the shell calls of one session, every nested session
// included, that succeeded and write to Git. A call that failed, was rejected
// or denied, whose command does not write Git, or a poll of a running command
// establishes nothing and has no window of its own.
func gitWrites(ctx context.Context, s sessions.Session) []gitWrite {
	g := s.Graph
	if g == nil {
		return nil
	}
	results := make(map[string]callOutcome, len(g.ToolResults))
	for _, r := range g.ToolResults {
		if r.Timestamp != nil {
			outcome := callOutcome{at: *r.Timestamp, status: r.Status}
			if r.OutputText != nil {
				outcome.text = *r.OutputText
			}
			results[r.ToolUseID] = outcome
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
	var polls []poll
	var writes []gitWrite
	for _, use := range g.ToolUses {
		result, ok := results[use.ID]
		if use.Verb != session.VerbExecute || use.Timestamp == nil || !ok ||
			result.status != session.StatusOK || (use.Status != "" && use.Status != session.StatusOK) {
			continue
		}
		base := attributeAt(g, use.SessionID, *use.Timestamp, "cwd")
		if base == "" {
			base = workingDirs[use.SessionID]
		}
		if base == "" && use.SessionID != top {
			base = attributeAt(g, top, *use.Timestamp, "cwd")
		}
		if base == "" {
			base = workingDirs[top]
		}
		if base == "" {
			base = s.WorktreeRoot
		}
		analysis, ok := analyzeCall(ctx, use.Name, use.Input, base)
		if !ok {
			continue
		}
		dirs := writeDirsOf(analysis)
		if len(dirs) == 0 {
			continue
		}
		commands := shellCommands(analysis)
		if boundToolName(use.Name, use.Input) == traceprocessing.CodexExecName && !codeModeOutcomeKnown(result.text, commands) {
			continue
		}
		if use.Name != "Bash" && reportsFailure(result.text) {
			continue
		}
		branch := branchAt(g, use.SessionID, top, *use.Timestamp)
		end := result.at
		failed := false
		if commands == 1 {
			// Only a call that ran one command reports that command's
			// process id; with several, a poll of another one (a test run
			// after the commit) must not keep the window open.
			if polls == nil {
				polls = pollsOf(g, results)
			}
			end, failed = windowEnd(*use.Timestamp, result.at, result.text, polls)
		}
		if failed {
			continue
		}
		slices.Sort(dirs)
		writes = append(writes, gitWrite{start: *use.Timestamp, end: end, dirs: slices.Compact(dirs), branch: branch})
	}
	return writes
}

// realPath is p with its symbolic links resolved (and, on Windows, its short
// names expanded), the form git records a working tree in: a session's
// directory may name the same place another way, such as macOS's /var for
// /private/var. A path that no longer exists, such as a removed worktree,
// resolves its deepest existing ancestor and keeps the rest; "" stays "".
func realPath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	rest := ""
	for d := p; ; {
		if resolved, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
		d = parent
	}
}

// nestedRepository reports whether dir lies in a repository of its own, such
// as a submodule or a clone inside the working tree: a directory between dir
// and root (the root excluded) that has a .git entry. The commit it makes is
// not in the working tree's reflog.
func nestedRepository(dir, root string) bool {
	if root == "" {
		return false
	}
	for d := filepath.Clean(dir); d != root; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	return false
}

// commandEngine analyzes shell tool calls. It is built once and holds no
// per-call state. It links no tree-sitter: inline Python and JavaScript are
// not analyzed, and Git commands are only ever run by shell.
var commandEngine = sync.OnceValues(func() (*agentcmd.Engine, error) {
	return agentcmd.New(agentcmd.Config{Rules: standard.Rules(), Native: nativestd.Bindings()})
})

// boundTools are the shell tools agentcmd reads natively: Claude Code's Bash,
// and Codex's exec_command, shell and code-mode exec, each with its own
// workdir.
var boundTools = map[string]bool{"Bash": true, "exec_command": true, "shell": true, traceprocessing.CodexExecName: true}

// gitWriteDirs are the directories in which one tool call runs a git
// subcommand that can create a commit, one for each such command, as agentcmd
// reads it: only commands that run count (in a command substitution or a
// script fed to a shell too), never quoted text or a heredoc that is only
// data; `cd`, `pushd`, `git -C`, `bash -c`, wrappers such as `env -C` and a
// Codex workdir move the directory, and one that cannot be known (`cd -`,
// `~`, a variable, a directory change inside a branch) leaves the command
// unplaced. A code-mode program contributes each call whose command and
// workdir are literal.
//
// Left out: a command run on another host or in a container, one run by
// find, one redirected with --git-dir, --work-tree or core.worktree, a
// --dry-run, every command of a call that mentions a variable of
// gitEnvNames (agentcmd does not follow them), and anything agentcmd cannot
// read.
func gitWriteDirs(ctx context.Context, name string, input json.RawMessage, base string) []string {
	analysis, ok := analyzeCall(ctx, name, input, base)
	if !ok {
		return nil
	}
	return writeDirsOf(analysis)
}

// analyzeCall runs agentcmd on one tool call; ok is false for a call that
// cannot run git as gitWriteDirs reads it, or that agentcmd cannot read.
func analyzeCall(ctx context.Context, name string, input json.RawMessage, base string) (model.Analysis, bool) {
	// Most calls run no git at all; agentcmd only names git from a literal
	// executable, so an input without the word cannot run it (about 30 µs
	// of analysis saved per call).
	if !bytes.Contains(input, []byte("git")) || mentionsGitEnv(input) {
		return model.Analysis{}, false
	}
	engine, err := commandEngine()
	if err != nil {
		return model.Analysis{}, false
	}
	execution := model.ExecutionContext{Namespace: model.Namespace{Kind: model.NamespaceHost}}
	if base != "" {
		execution.WorkingDir = model.KnownFact(filepath.ToSlash(base), "transcript")
	}
	var analysis model.Analysis
	if tool := boundToolName(name, input); tool != "" {
		analysis, err = engine.Analyze(ctx, model.Invocation{
			ID:      "call",
			Tool:    model.ToolIdentity{Namespace: "harness", Name: tool},
			Input:   model.Input{Kind: model.InputJSON, JSON: input},
			Context: execution,
		})
	} else {
		command := commandText(input)
		if command == "" {
			return model.Analysis{}, false
		}
		var shape struct {
			Workdir string `json:"workdir"`
		}
		if json.Unmarshal(input, &shape) == nil && shape.Workdir != "" {
			execution.WorkingDir = model.Fact{}
			if dir := resolveDir(base, shape.Workdir); dir != "" {
				execution.WorkingDir = model.KnownFact(filepath.ToSlash(dir), "transcript")
			}
		}
		analysis, err = engine.AnalyzeShell(ctx, []byte(command), execution)
	}
	if err != nil {
		return model.Analysis{}, false
	}
	return analysis, true
}

// shellCommands counts the commands a tool call runs: the shell programs and
// the unreadable calls directly under its native operation (several for a
// code-mode program), or one for a call read as shell text.
func shellCommands(analysis model.Analysis) int {
	native := ""
	for _, n := range analysis.Nodes {
		if n.Kind == model.NodeNative && native == "" {
			native = n.ID
		}
	}
	if native == "" {
		return 1
	}
	count := 0
	for _, n := range analysis.Nodes {
		if n.Parent == native && (n.Kind == model.NodeShell || n.Kind == model.NodeUnknown) {
			count++
		}
	}
	return count
}

// exitCode finds the exit status a shell tool reports for a command that
// finished: `"exit_code":N` in a structured result (code-mode programs print
// these, JSON-escaped once more inside their text), `Process exited with code
// N` in the text of a native one.
var exitCode = regexp.MustCompile(`\\?"exit_code\\?"\s*:\s*(-?\d+)|Process exited with code (-?\d+)`)

// reportsFailure reports whether a result says one of its commands exited
// with a non-zero status.
func reportsFailure(text string) bool {
	for _, m := range exitCode.FindAllStringSubmatch(text, -1) {
		if m[1]+m[2] != "0" {
			return true
		}
	}
	return false
}

// codeModeOutcomeKnown reports whether a code-mode program's result shows how
// its commands went. The program succeeding says nothing about them; only the
// results it prints do: none failed, and each finished (an exit status) or is
// still running (a process id). A program that printed fewer results than it
// ran commands, or failed ones, gives no evidence.
func codeModeOutcomeKnown(text string, commands int) bool {
	if reportsFailure(text) {
		return false
	}
	return len(exitCode.FindAllStringIndex(text, -1))+len(processIDs(text)) >= commands
}

// boundToolName is the agentcmd tool that reads the call, "" for a tool
// agentcmd has no binding for. A Codex exec whose input carries a "cmd" is an
// exec_command, not a code-mode program.
func boundToolName(name string, input json.RawMessage) string {
	if strings.EqualFold(name, traceprocessing.CodexExecName) {
		if _, ok := codeModeSource(input); !ok {
			return "exec_command"
		}
		return traceprocessing.CodexExecName
	}
	if boundTools[name] {
		return name
	}
	return ""
}

// gitEnvNames are the environment variables that move git to another
// repository or working tree: GIT_DIR, GIT_WORK_TREE, GIT_COMMON_DIR, and the
// GIT_CONFIG_* ones, which can set core.worktree.
var gitEnvNames = [][]byte{[]byte("GIT_DIR"), []byte("GIT_WORK_TREE"), []byte("GIT_COMMON_DIR"), []byte("GIT_CONFIG")}

// mentionsGitEnv reports whether a call may set one of gitEnvNames, which
// agentcmd does not follow: the name appears in the input, or in any string it
// holds once JSON escapes are decoded and then backslash escapes too (the
// JavaScript ones of a code-mode program, and bash's $'...').
func mentionsGitEnv(input json.RawMessage) bool {
	named := func(s []byte) bool {
		return slices.ContainsFunc(gitEnvNames, func(name []byte) bool { return bytes.Contains(s, name) })
	}
	if named(input) {
		return true
	}
	var value any
	if json.Unmarshal(input, &value) != nil {
		return false
	}
	found := false
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			if named([]byte(v)) || named([]byte(unescapeBackslashes(v))) {
				found = true
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		case map[string]any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(value)
	return found
}

// unescapeBackslashes decodes the backslash escapes of a JavaScript string or
// a bash $'...' string, wherever they appear: hex (x), Unicode (u with four
// digits or braces), line continuations, and identity escapes, which stand
// for the character itself. Control escapes become a space. It only serves to
// spot names spelled through escapes.
func unescapeBackslashes(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var out strings.Builder
	hex := func(digits string) (rune, bool) {
		n, err := strconv.ParseUint(digits, 16, 32)
		return rune(n), err == nil && digits != ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case '\n':
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case 'x':
			if r, ok := hex(s[i+1 : min(i+3, len(s))]); ok && i+2 < len(s) {
				out.WriteRune(r)
				i += 2
				continue
			}
			out.WriteByte(c)
		case 'u':
			if i+1 < len(s) && s[i+1] == '{' {
				if end := strings.IndexByte(s[i:], '}'); end > 0 {
					if r, ok := hex(s[i+2 : i+end]); ok {
						out.WriteRune(r)
						i += end
						continue
					}
				}
			} else if r, ok := hex(s[i+1 : min(i+5, len(s))]); ok && i+4 < len(s) {
				out.WriteRune(r)
				i += 4
				continue
			}
			out.WriteByte(c)
		case 'n', 't', 'r', 'b', 'f', 'v', '0':
			out.WriteByte(' ')
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// writeDirsOf reads the creating git commands of an analysis. Only commands
// run on this host count: agentcmd follows ssh, docker and kubectl into a
// remote or container namespace, whose paths are not this clone's. A command
// run by find (-exec or -execdir) is left out, since -execdir runs it in each
// match's directory.
func writeDirsOf(analysis model.Analysis) []string {
	nodes := make(map[string]*model.Node, len(analysis.Nodes))
	for i := range analysis.Nodes {
		nodes[analysis.Nodes[i].ID] = &analysis.Nodes[i]
	}
	var dirs []string
	for _, action := range analysis.Actions {
		if action.Git == nil || !gitWriteCommands[action.Git.Subcommand] {
			continue
		}
		node := nodes[action.Node]
		if node == nil || node.Command == nil || node.Reach == model.ReachUnreachable ||
			node.Context.Namespace.Kind != model.NamespaceHost || slices.Contains(node.Command.Wrappers, "find") ||
			redirected(node.Command.Args) {
			continue
		}
		for _, r := range action.Resources {
			if r.Role != "repository" || r.Namespace.Kind != model.NamespaceHost {
				continue
			}
			if dir, ok := hostDir(r.Resolved); ok {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

// hostDir is a resolved directory as a path of this host. agentcmd resolves
// in slash form, so a backslash means a Windows path it took as relative, and
// a "~", "$" or "+" segment one it joined literally (`workdir: "~/r"`,
// `pushd +1`); those are unknown.
func hostDir(resolved string) (string, bool) {
	if resolved == "" || strings.Contains(resolved, `\`) {
		return "", false
	}
	clean := path.Clean(resolved)
	for i, segment := range strings.Split(clean, "/") {
		if segment != "" && strings.ContainsAny(segment[:1], "~$+") || i > 0 && strings.Contains(segment, ":") {
			return "", false
		}
	}
	dir := filepath.FromSlash(clean)
	if !filepath.IsAbs(dir) {
		return "", false
	}
	return dir, true
}

// gitValueOptions are git's global options that take the next argument as
// their value.
var gitValueOptions = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--config-env": true}

// movingConfig reports whether a `-c` or `--config-env` setting can move git
// to another working tree: core.worktree itself, or an include of a file that
// may set it.
func movingConfig(setting string) bool {
	key := strings.ToLower(setting)
	return strings.HasPrefix(key, "core.worktree") || strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.")
}

// gitInfoOption reports whether a global option makes git print something and
// exit before any subcommand runs.
func gitInfoOption(arg string) bool {
	return arg == "--exec-path" || arg == "--version" || arg == "--help" || arg == "-h" ||
		arg == "--html-path" || arg == "--man-path" || arg == "--info-path" || strings.HasPrefix(arg, "--list-cmds")
}

// redirected reports whether git's global options point it at another
// repository or working tree or are not known, or the subcommand's arguments
// ask for a dry run.
func redirected(args []model.Value) bool {
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		switch {
		case !arg.Concrete():
			return true
		case arg.Text == "--git-dir" || arg.Text == "--work-tree" ||
			strings.HasPrefix(arg.Text, "--git-dir=") || strings.HasPrefix(arg.Text, "--work-tree="):
			return true
		case gitInfoOption(arg.Text):
			return true // prints and exits: no commit
		case strings.HasPrefix(arg.Text, "--config-env="):
			if movingConfig(strings.TrimPrefix(arg.Text, "--config-env=")) {
				return true
			}
			continue
		case gitValueOptions[arg.Text]:
			// The value must be known; `-c core.worktree=...` moves the
			// working tree like --work-tree does.
			i++
			if i >= len(args) || !args[i].Concrete() {
				return true
			}
			if (arg.Text == "-c" || arg.Text == "--config-env") && movingConfig(args[i].Text) {
				return true
			}
			continue
		case strings.HasPrefix(arg.Text, "-"):
			continue
		}
		break
	}
	for _, arg := range args[min(i+1, len(args)):] {
		if arg.Text == "--dry-run" {
			return true
		}
	}
	return false
}

// codeModeSource is the JavaScript program of a Codex code-mode input: a JSON
// string, or an object holding it as "input". The stored form is the object
// {"input": <program>, "command": <command lines>}, whose command is derived
// from the program, so only a "cmd" marks an input that is not code-mode.
func codeModeSource(input json.RawMessage) (string, bool) {
	var source string
	if json.Unmarshal(input, &source) == nil {
		return source, true
	}
	var object struct {
		Input string          `json:"input"`
		Cmd   json.RawMessage `json:"cmd"`
	}
	if json.Unmarshal(input, &object) == nil && object.Input != "" && object.Cmd == nil {
		return object.Input, true
	}
	return "", false
}

// resolveDir is target seen from dir, "" when it cannot be known.
func resolveDir(dir, target string) string {
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

// attributeAt is the value of a string attribute of one session in effect at
// time at, "" when unset. keys are the spellings the vendors use.
func attributeAt(g *session.Graph, id string, at time.Time, keys ...string) string {
	var latest *session.SessionAttribute
	for i := range g.Attributes {
		a := &g.Attributes[i]
		if a.SessionID == id && slices.Contains(keys, a.Key) && !a.ValidFrom.After(at) && (latest == nil || !a.ValidFrom.Before(latest.ValidFrom)) {
			latest = a
		}
	}
	if latest == nil {
		return ""
	}
	var value string
	if json.Unmarshal(latest.Value, &value) != nil {
		return ""
	}
	return value
}

// branchAt is the branch a call ran on: the nested session's own record, then
// the top-level session's, from the branch attribute in effect and else the
// session's recorded branch. "" when unknown.
func branchAt(g *session.Graph, nested, top string, at time.Time) string {
	ids := []string{nested}
	if top != nested {
		ids = append(ids, top)
	}
	for _, id := range ids {
		if branch := attributeAt(g, id, at, "gitBranch", "git.branch"); branch != "" {
			return branch
		}
		for _, s := range g.Sessions {
			if s.ID == id && s.GitBranch != nil && *s.GitBranch != "" {
				return *s.GitBranch
			}
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
