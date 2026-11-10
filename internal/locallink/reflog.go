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
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/linkage/gitevidence"
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
	// maxShellDepth bounds how deep `bash -c` scripts nest.
	maxShellDepth = 4
)

// creationMessage matches the reflog messages of commits made in a working
// tree: commits (also the one that finishes a cherry-pick), amends,
// cherry-picks, reverts, applied patches and the picks of a rebase (also those
// of "pull --rebase"). Merges are left out on purpose: the matcher excludes
// merge commits.
var creationMessage = regexp.MustCompile(`^(commit( \((amend|initial|cherry-pick)\))?|cherry-pick|revert|am|[^:]*\((pick|continue|squash|fixup|edit|reword)\)): `)

// gitWriteCommands are the git subcommands that can create a commit.
var gitWriteCommands = map[string]bool{"commit": true, "rebase": true, "cherry-pick": true, "revert": true, "am": true, "pull": true}

// gitEnvVars redirect git to another repository or working tree.
var gitEnvVars = map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true}

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
	add(reflogFile{path: filepath.Join(common, "logs", "HEAD"), head: true, worktree: mainRootOf(common)})
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
			add(reflogFile{path: filepath.Join(admin, "logs", "HEAD"), head: true, worktree: filepath.Dir(filepath.Clean(gitdir))})
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
// reflogs cannot be read fingerprints as having no entries.
func ReflogFingerprint(ctx context.Context, dir string, commits []string) string {
	wanted := make(map[string]bool, len(commits))
	for _, sha := range commits {
		wanted[sha] = true
	}
	log := readReflog(ctx, dir, wanted)
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
// observations, which guess the session from the process tree. Unreadable
// reflogs give no evidence.
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

// callWindows lists the shell calls of one session, every nested session
// included, that succeeded and write to Git. A call that failed, was rejected
// or denied, whose command does not write Git, or a poll of a running command
// establishes nothing and has no window.
func callWindows(s sessions.Session, locate func(string) string) []callWindow {
	g := s.Graph
	if g == nil {
		return nil
	}
	type outcome struct {
		at     time.Time
		status session.ToolStatus
	}
	results := make(map[string]outcome, len(g.ToolResults))
	for _, r := range g.ToolResults {
		if r.Timestamp != nil {
			results[r.ToolUseID] = outcome{at: *r.Timestamp, status: r.Status}
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
		branch := branchAt(g, use.SessionID, top, *use.Timestamp)
		seen := map[string]bool{}
		for _, c := range shellCalls(use.Name, use.Input, base) {
			for _, dir := range writeDirs(c.command, c.dir) {
				if seen[dir] {
					continue
				}
				seen[dir] = true
				windows = append(windows, callWindow{session: s.ID, start: *use.Timestamp, end: result.at, root: locate(dir), branch: branch})
			}
		}
	}
	return windows
}

// shellCall is one command a tool call ran, and the directory it started in
// ("" when unknown).
type shellCall struct {
	command, dir string
}

// shellCalls are the commands of one tool call, each with its own directory.
// A Codex code-mode program holds several, each with its workdir, and one with
// a command or workdir built at run time is left out. A call without command
// text, such as a poll of a running command, has none.
func shellCalls(name string, input json.RawMessage, base string) []shellCall {
	if strings.EqualFold(name, traceprocessing.CodexExecName) {
		if source, ok := codeModeSource(input); ok {
			var calls []shellCall
			for _, c := range traceprocessing.CodexExecCalls(source) {
				if !c.CommandStatic || (c.HasWorkdir && !c.WorkdirStatic) {
					continue
				}
				dir := base
				if c.HasWorkdir {
					dir = resolveDir(base, c.Workdir)
				}
				calls = append(calls, shellCall{command: c.Command, dir: dir})
			}
			return calls
		}
	}
	command := commandText(input)
	if command == "" {
		return nil
	}
	dir := base
	var shape struct {
		Workdir string `json:"workdir"`
	}
	if json.Unmarshal(input, &shape) == nil && shape.Workdir != "" {
		dir = resolveDir(base, shape.Workdir)
	}
	return []shellCall{{command: command, dir: dir}}
}

// codeModeSource is the JavaScript program of a Codex code-mode input: a JSON
// string, or an object holding it as "input" and no command of its own.
func codeModeSource(input json.RawMessage) (string, bool) {
	var source string
	if json.Unmarshal(input, &source) == nil {
		return source, true
	}
	var object struct {
		Input   string          `json:"input"`
		Cmd     json.RawMessage `json:"cmd"`
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(input, &object) == nil && object.Input != "" && object.Cmd == nil && object.Command == nil {
		return object.Input, true
	}
	return "", false
}

// shellScope is what a script has done to the directory it runs in.
type shellScope struct {
	// dir is the current directory, "" when unknown.
	dir string
	// gitEnv is set once GIT_DIR or GIT_WORK_TREE was set.
	gitEnv bool
}

// writeDirs are the directories in which the command runs a git subcommand
// that can create a commit, one for each such command, or none when the
// command does not parse. Only commands count: quoted text, heredoc bodies and
// substitutions never do. A `cd` or `pushd` to a literal path moves the
// directory within its scope (a subshell has its own); `cd -`, `~`, a variable
// or a dynamic path leaves it unknown, so a later git command is unplaced. So
// is one redirected with GIT_DIR, --git-dir or --work-tree, unless `-C` names
// an absolute directory.
func writeDirs(command, dir string) []string {
	var dirs []string
	scanScript(command, shellScope{dir: dir}, &dirs, 0)
	return dirs
}

func scanScript(script string, scope shellScope, out *[]string, depth int) {
	if depth > maxShellDepth {
		return
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return
	}
	scanStmts(file.Stmts, &scope, out, depth)
}

func scanStmts(stmts []*syntax.Stmt, scope *shellScope, out *[]string, depth int) {
	for _, stmt := range stmts {
		scanStmt(stmt, scope, out, depth)
	}
}

func scanStmt(stmt *syntax.Stmt, scope *shellScope, out *[]string, depth int) {
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		scanCall(cmd, scope, out, depth)
	case *syntax.BinaryCmd:
		if cmd.Op == syntax.Pipe || cmd.Op == syntax.PipeAll {
			left, right := *scope, *scope
			scanStmt(cmd.X, &left, out, depth)
			scanStmt(cmd.Y, &right, out, depth)
			return
		}
		scanStmt(cmd.X, scope, out, depth)
		scanStmt(cmd.Y, scope, out, depth)
	case *syntax.Subshell:
		inner := *scope
		scanStmts(cmd.Stmts, &inner, out, depth)
	case *syntax.Block:
		scanStmts(cmd.Stmts, scope, out, depth)
	case *syntax.DeclClause:
		for _, assign := range cmd.Args {
			if assign.Name != nil && gitEnvVars[assign.Name.Value] {
				scope.gitEnv = true
			}
		}
	case *syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause, *syntax.TimeClause:
		// Which branches run is unknown, so each starts from the current
		// directory and a directory change inside leaves it unknown after.
		for _, list := range clauseStmts(cmd) {
			inner := *scope
			scanStmts(list, &inner, out, depth)
		}
		if changesDirectory(stmt) {
			scope.dir = ""
		}
	}
}

// clauseStmts are the statement lists of a compound command.
func clauseStmts(cmd syntax.Command) [][]*syntax.Stmt {
	switch cmd := cmd.(type) {
	case *syntax.IfClause:
		var lists [][]*syntax.Stmt
		for clause := cmd; clause != nil; clause = clause.Else {
			lists = append(lists, clause.Cond, clause.Then)
		}
		return lists
	case *syntax.WhileClause:
		return [][]*syntax.Stmt{cmd.Cond, cmd.Do}
	case *syntax.ForClause:
		return [][]*syntax.Stmt{cmd.Do}
	case *syntax.CaseClause:
		var lists [][]*syntax.Stmt
		for _, item := range cmd.Items {
			lists = append(lists, item.Stmts)
		}
		return lists
	case *syntax.TimeClause:
		if cmd.Stmt != nil {
			return [][]*syntax.Stmt{{cmd.Stmt}}
		}
	}
	return nil
}

func changesDirectory(node syntax.Node) bool {
	changes := false
	syntax.Walk(node, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			switch call.Args[0].Lit() {
			case "cd", "pushd", "popd":
				changes = true
			}
		}
		return !changes
	})
	return changes
}

func scanCall(call *syntax.CallExpr, scope *shellScope, out *[]string, depth int) {
	envGit := false
	for _, assign := range call.Assigns {
		if assign.Name != nil && gitEnvVars[assign.Name.Value] {
			envGit = true
		}
	}
	if len(call.Args) == 0 {
		if envGit {
			scope.gitEnv = true
		}
		return
	}
	words := make([]string, len(call.Args))
	static := make([]bool, len(call.Args))
	for i, arg := range call.Args {
		words[i], static[i] = gitevidence.StaticWord(arg)
	}
	if !static[0] {
		return
	}
	switch path.Base(words[0]) {
	case "cd":
		scope.dir = changeDir(scope.dir, words[1:], static[1:], true)
	case "pushd":
		scope.dir = changeDir(scope.dir, words[1:], static[1:], false)
	case "popd":
		scope.dir = ""
	case "git":
		if dir, ok := gitWriteDir(words[1:], static[1:], *scope, envGit); ok {
			*out = append(*out, dir)
		}
	case "bash", "sh", "zsh":
		for i := 1; i < len(words); i++ {
			if !static[i] || !strings.HasPrefix(words[i], "-") {
				return
			}
			if !strings.HasPrefix(words[i], "--") && strings.Contains(words[i], "c") {
				if i+1 < len(words) && static[i+1] {
					scanScript(words[i+1], *scope, out, depth+1)
				}
				return
			}
		}
	}
}

// changeDir is the directory after "cd" or "pushd" with the arguments, ""
// when it cannot be known. Only cd takes the -P and -L flags.
func changeDir(dir string, words []string, static []bool, cd bool) string {
	for len(words) > 0 && static[0] && strings.HasPrefix(words[0], "-") && words[0] != "-" {
		if !cd {
			return ""
		}
		words, static = words[1:], static[1:]
	}
	if len(words) == 0 || !static[0] || strings.HasPrefix(words[0], "+") {
		return ""
	}
	return resolveDir(dir, words[0])
}

// gitWriteDir reads the arguments of a git command: it reports the directory
// the command runs in and whether its subcommand can create a commit. The
// subcommand is the first word that is not an option or an option's value.
func gitWriteDir(args []string, static []bool, scope shellScope, envGit bool) (string, bool) {
	dir := scope.dir
	redirected, anchored := scope.gitEnv || envGit, false
	for i := 0; i < len(args); i++ {
		if !static[i] {
			return "", false
		}
		arg := args[i]
		switch {
		case arg == "-C":
			if i+1 >= len(args) || !static[i+1] {
				return "", false
			}
			dir = resolveDir(dir, args[i+1])
			anchored = filepath.IsAbs(args[i+1])
			i++
		case arg == "--git-dir" || arg == "--work-tree":
			redirected = true
			i++
		case strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree="):
			redirected = true
		case arg == "-c" || arg == "--namespace" || arg == "--exec-path" || arg == "--config-env":
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			if !gitWriteCommands[arg] || (redirected && !anchored) || slices.Contains(args[i+1:], "--dry-run") {
				return "", false
			}
			return dir, dir != ""
		}
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
