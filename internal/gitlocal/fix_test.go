package gitlocal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixFixture is a bare remote with main and a feature branch that conflict
// in a.txt, and a clone of it made before either moved on.
type fixFixture struct {
	remote, clone, seed string
	head, base          string
}

func newFixFixture(t *testing.T) fixFixture {
	t.Helper()
	requireGit(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, t.TempDir(), "clone", "-q", remote, seed)
	git(t, seed, "checkout", "-q", "-b", "main")
	write(t, seed, "a.txt", "one\n", 0o644)
	commit(t, seed, "initial")
	git(t, seed, "push", "-q", "origin", "main")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, t.TempDir(), "clone", "-q", remote, clone)
	git(t, seed, "checkout", "-q", "-b", "feature")
	write(t, seed, "a.txt", "feature\n", 0o644)
	head := commit(t, seed, "feature change")
	git(t, seed, "push", "-q", "origin", "feature")
	git(t, seed, "checkout", "-q", "main")
	write(t, seed, "a.txt", "main\n", 0o644)
	base := commit(t, seed, "main change")
	git(t, seed, "push", "-q", "origin", "main")
	return fixFixture{remote: remote, clone: clone, seed: seed, head: head, base: base}
}

func startFix(t *testing.T, f fixFixture, opts FixOptions) (*Fix, error) {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = filepath.Join(t.TempDir(), "fix")
	}
	if opts.ID == "" {
		opts.ID = "job1"
	}
	if opts.HeadRef == "" {
		opts.HeadRef = "feature"
	}
	if opts.HeadSHA == "" {
		opts.HeadSHA = f.head
	}
	return open(t, f.clone, Options{}).StartFix(context.Background(), opts)
}

func TestFixMergesCommitsAndPushesWithoutForce(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if fix.Base != f.base || readFile(t, filepath.Join(fix.Dir(), "a.txt")) != "feature\n" {
		t.Fatalf("checkout at the wrong commit: base %s", fix.Base)
	}
	// A hook in the clone must never run.
	write(t, f.clone, ".git/hooks/pre-commit", "#!/bin/sh\ntouch \"$GIT_DIR/../../hook-ran\"\nexit 1\n", 0o755)
	write(t, f.clone, ".git/hooks/pre-push", "#!/bin/sh\nexit 1\n", 0o755)
	conflicts, err := fix.Merge(ctx, "Merge main into feature")
	if err != nil || len(conflicts) != 1 || conflicts[0] != "a.txt" {
		t.Fatalf("conflicts %q, %v", conflicts, err)
	}
	if !strings.Contains(readFile(t, filepath.Join(fix.Dir(), "a.txt")), "<<<<<<<") {
		t.Fatal("the conflict markers are not in the checkout")
	}
	write(t, fix.Dir(), "a.txt", "feature and main\n", 0o644)
	// A .git file the agent rewrote is ignored.
	write(t, fix.Dir(), ".git", "gitdir: /nowhere\n", 0o644)
	merged, err := fix.Commit(ctx, "Merge main into feature")
	if err != nil {
		t.Fatal(err)
	}
	if parents := git(t, f.clone, "rev-list", "--parents", "-n", "1", merged); parents != merged+" "+f.head+" "+f.base {
		t.Fatalf("not a merge of head and base: %s", parents)
	}
	if ok, err := fix.Descends(ctx, merged); !ok || err != nil {
		t.Fatalf("descends %v %v", ok, err)
	}
	if err := fix.Push(ctx, merged); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.remote, "rev-parse", "refs/heads/feature"); got != merged {
		t.Fatalf("remote feature at %s, want %s", got, merged)
	}
	if err := fix.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fix.dir); !os.IsNotExist(err) {
		t.Fatalf("the fix directory remains: %v", err)
	}
	if list := git(t, f.clone, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("a worktree is registered in the clone:\n%s", list)
	}
	if refs := git(t, f.clone, "for-each-ref", "refs/baum/"); refs != "" {
		t.Fatalf("temporary refs remain: %s", refs)
	}
}

func TestFixRefusesAMovedBranchAndARejectedPush(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	stale := git(t, f.seed, "rev-parse", "main~1")
	dir := filepath.Join(t.TempDir(), "fix")
	if _, err := startFix(t, f, FixOptions{Dir: dir, HeadSHA: stale}); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err %v, want ErrHeadMoved", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a refused fix left its directory")
	}

	fix, err := startFix(t, f, FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fix.Close() }()
	if head, err := fix.Commit(ctx, "nothing"); err != nil || head != f.head {
		t.Fatalf("an empty commit was made: %s %v", head, err)
	}
	write(t, fix.Dir(), "b.txt", "fixed\n", 0o644)
	fixed, err := fix.Commit(ctx, "Fix the build")
	if err != nil || fixed == f.head {
		t.Fatalf("commit %s %v", fixed, err)
	}
	// The branch moves on while the fix runs: the push is refused, not forced.
	git(t, f.seed, "checkout", "-q", "feature")
	write(t, f.seed, "c.txt", "someone else\n", 0o644)
	moved := commit(t, f.seed, "someone else")
	git(t, f.seed, "push", "-q", "origin", "feature")
	if err := fix.Push(ctx, fixed); !errors.Is(err, ErrPushRejected) {
		t.Fatalf("err %v, want ErrPushRejected", err)
	}
	if got := git(t, f.remote, "rev-parse", "refs/heads/feature"); got != moved {
		t.Fatal("the push replaced someone else's commit")
	}
	// A commit that drops the head, such as its parent, is not a descendant.
	if ok, err := fix.Descends(ctx, stale); ok || err != nil {
		t.Fatalf("the head's parent descends from it: %v %v", ok, err)
	}
}

func TestFixChecksSigningWithTheAgentEnvironment(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	// The user's own global configuration signs nothing; the agent's signs
	// with a program that always fails, as gpg does when it cannot prompt.
	plain := filepath.Join(t.TempDir(), "plain.gitconfig")
	write(t, filepath.Dir(plain), "plain.gitconfig", "[user]\n\tname = Ada\n\temail = ada@example.com\n", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", plain)
	signing := filepath.Join(t.TempDir(), "agent.gitconfig")
	write(t, filepath.Dir(signing), "agent.gitconfig", "[include]\n\tpath = "+filepath.ToSlash(plain)+"\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = false\n", 0o644)

	fix, err := startFix(t, f, FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fix.CheckSigning(ctx); err != nil {
		t.Fatalf("a configuration that does not sign must pass: %v", err)
	}
	if err := fix.Close(); err != nil {
		t.Fatal(err)
	}

	fix, err = startFix(t, f, FixOptions{ID: "job2", Env: []string{"GIT_CONFIG_GLOBAL=" + signing, "GIT_DIR=/nowhere"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fix.Close() }()
	if err := fix.CheckSigning(ctx); err == nil {
		t.Fatal("signing with a failing program passed the check")
	}
	if head := agentHEAD(fix.GitDir()); head != f.head {
		t.Fatalf("the check moved the agent's HEAD to %s, or GIT_DIR from the agent environment was used", head)
	}
}

// addedText is the text of a change's added lines, one per line.
func addedText(c Change) string {
	var parts []string
	for _, r := range c.Added {
		parts = append(parts, c.Content[r[0]:r[1]])
	}
	return strings.Join(parts, "|")
}

func changesByPath(t *testing.T, fix *Fix, commit string) map[string]Change {
	t.Helper()
	list, err := fix.Changes(context.Background(), commit)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Change{}
	for _, c := range list {
		out[c.Path] = c
	}
	return out
}

// Changes reports the fix's own lines and files, and a nested repository the
// agent made as the gitlink git add records for it.
func TestFixChangesReportsAddedLinesAndNestedRepositories(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	fix, err := startFix(t, f, FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fix.Close() }()
	write(t, fix.Dir(), "a.txt", "feature\nadded\n", 0o644)
	write(t, fix.Dir(), "new/key.txt", "one\ntwo\n", 0o644)
	write(t, fix.Dir(), "blob.bin", "a\x00b", 0o644)
	nested := filepath.Join(fix.Dir(), "nested")
	git(t, fix.Dir(), "init", "-q", nested)
	write(t, nested, "x.txt", "x\n", 0o644)
	commit(t, nested, "nested")
	head, err := fix.Commit(ctx, "Fix")
	if err != nil {
		t.Fatal(err)
	}
	changes := changesByPath(t, fix, head)
	if len(changes) != 4 {
		t.Fatalf("changes %+v", changes)
	}
	if c := changes["a.txt"]; c.Mode != ModeFile || addedText(c) != "added" {
		t.Fatalf("a.txt %+v", c)
	}
	if c := changes["new/key.txt"]; addedText(c) != "one\ntwo" {
		t.Fatalf("new/key.txt %+v", c)
	}
	if c := changes["blob.bin"]; !c.Binary || c.Content != "" {
		t.Fatalf("blob.bin %+v", c)
	}
	if c := changes["nested"]; c.Mode != ModeGitlink {
		t.Fatalf("nested %+v", c)
	}
}

// After a merge, what the base branch brought is not the fix's own work.
func TestFixChangesLeaveOutWhatAMergeBrought(t *testing.T) {
	t.Run("conflicts", func(t *testing.T) {
		f := newFixFixture(t)
		ctx := context.Background()
		write(t, f.seed, "base.txt", "from main\n", 0o644)
		commit(t, f.seed, "another main change")
		git(t, f.seed, "push", "-q", "origin", "main")
		fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fix.Close() }()
		if conflicts, err := fix.Merge(ctx, "Merge"); err != nil || len(conflicts) != 1 {
			t.Fatalf("conflicts %v %v", conflicts, err)
		}
		write(t, fix.Dir(), "a.txt", "feature\nmain\nagent\n", 0o644)
		head, err := fix.Commit(ctx, "Merge")
		if err != nil {
			t.Fatal(err)
		}
		changes := changesByPath(t, fix, head)
		if len(changes) != 1 || addedText(changes["a.txt"]) != "agent" {
			t.Fatalf("changes %+v", changes)
		}
	})
	t.Run("clean", func(t *testing.T) {
		f := newFixFixture(t)
		ctx := context.Background()
		git(t, f.seed, "checkout", "-q", "-b", "clean", "main~1")
		write(t, f.seed, "c.txt", "clean\n", 0o644)
		head := commit(t, f.seed, "clean change")
		git(t, f.seed, "push", "-q", "origin", "clean")
		fix, err := startFix(t, f, FixOptions{HeadRef: "clean", HeadSHA: head, BaseRef: "main"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fix.Close() }()
		if conflicts, err := fix.Merge(ctx, "Merge"); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts %v %v", conflicts, err)
		}
		write(t, fix.Dir(), "d.txt", "agent\n", 0o644)
		result, err := fix.Commit(ctx, "Fix")
		if err != nil {
			t.Fatal(err)
		}
		changes := changesByPath(t, fix, result)
		if len(changes) != 1 || addedText(changes["d.txt"]) != "agent" {
			t.Fatalf("changes %+v", changes)
		}
	})
}

// The agent's repository has the branch checked out at the head, the base as
// origin/<base> and the clone's objects, and lives outside the checkout.
func TestFixGivesTheAgentARepositoryOfItsOwn(t *testing.T) {
	f := newFixFixture(t)
	fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fix.Close() }()
	if got := git(t, fix.Dir(), "rev-parse", "--absolute-git-dir"); resolveExisting(got) != resolveExisting(fix.GitDir()) || inside(fix.Dir(), fix.GitDir()) {
		t.Fatalf("git directory %s, want %s outside the checkout", got, fix.GitDir())
	}
	if branch := git(t, fix.Dir(), "symbolic-ref", "--short", "HEAD"); branch != "feature" {
		t.Fatalf("branch %s", branch)
	}
	if head, base := git(t, fix.Dir(), "rev-parse", "HEAD"), git(t, fix.Dir(), "rev-parse", "origin/main"); head != f.head || base != f.base {
		t.Fatalf("HEAD %s origin/main %s", head, base)
	}
	if status := git(t, fix.Dir(), "status", "--porcelain"); status != "" {
		t.Fatalf("the checkout is not clean: %s", status)
	}
	if hooks, _ := os.ReadDir(filepath.Join(fix.GitDir(), "hooks")); len(hooks) != 0 {
		t.Fatalf("the agent's repository has hooks from a template: %v", hooks)
	}
}

// What the agent did with git decides the commit's parents; the tree is the
// checkout's whatever the agent committed.
func TestFixCommitsWhatTheAgentLeft(t *testing.T) {
	t.Run("merge of the base", func(t *testing.T) {
		f := newFixFixture(t)
		ctx := context.Background()
		fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fix.Close() }()
		// The agent merges origin/main itself, resolves the conflict and
		// commits, then changes one more file without committing it.
		cmd := exec.Command("git", "merge", "origin/main")
		cmd.Dir = fix.Dir()
		_ = cmd.Run()
		write(t, fix.Dir(), "a.txt", "feature and main\n", 0o644)
		commit(t, fix.Dir(), "agent merge")
		write(t, fix.Dir(), "b.txt", "fix\n", 0o644)
		if merged, err := fix.Merged(ctx); err != nil || !merged {
			t.Fatalf("merged %v %v", merged, err)
		}
		result, err := fix.Commit(ctx, "Merge and fix")
		if err != nil {
			t.Fatal(err)
		}
		if parents := git(t, f.clone, "rev-list", "--parents", "-n", "1", result); parents != result+" "+f.head+" "+f.base {
			t.Fatalf("not baum's merge of head and base: %s", parents)
		}
		if got := git(t, f.clone, "show", result+":b.txt"); got != "fix" {
			t.Fatalf("b.txt %q", got)
		}
		if got := git(t, f.clone, "log", "-1", "--format=%s", result); got != "Merge and fix" {
			t.Fatalf("message %q", got)
		}
		changes := changesByPath(t, fix, result)
		if len(changes) != 2 || addedText(changes["a.txt"]) != "feature and main" || addedText(changes["b.txt"]) != "fix" {
			t.Fatalf("changes %+v", changes)
		}
	})
	t.Run("merge left uncommitted", func(t *testing.T) {
		f := newFixFixture(t)
		ctx := context.Background()
		fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fix.Close() }()
		cmd := exec.Command("git", "merge", "origin/main")
		cmd.Dir = fix.Dir()
		_ = cmd.Run()
		write(t, fix.Dir(), "a.txt", "resolved\n", 0o644)
		result, err := fix.Commit(ctx, "Merge")
		if err != nil {
			t.Fatal(err)
		}
		if parents := git(t, f.clone, "rev-list", "--parents", "-n", "1", result); parents != result+" "+f.head+" "+f.base {
			t.Fatalf("not a merge of head and base: %s", parents)
		}
	})
	t.Run("commits on the head", func(t *testing.T) {
		f := newFixFixture(t)
		ctx := context.Background()
		fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fix.Close() }()
		write(t, fix.Dir(), "b.txt", "one\n", 0o644)
		commit(t, fix.Dir(), "agent one")
		write(t, fix.Dir(), "c.txt", "two\n", 0o644)
		commit(t, fix.Dir(), "agent two")
		// A branch the agent made and left is of no account.
		git(t, fix.Dir(), "branch", "scratch", "origin/main")
		result, err := fix.Commit(ctx, "Fix")
		if err != nil {
			t.Fatal(err)
		}
		if parents := git(t, f.clone, "rev-list", "--parents", "-n", "1", result); parents != result+" "+f.head {
			t.Fatalf("not one commit on the head: %s", parents)
		}
		if files := git(t, f.clone, "diff-tree", "--no-commit-id", "--name-only", "-r", f.head, result); files != "b.txt\nc.txt" {
			t.Fatalf("files %q", files)
		}
	})
	t.Run("reset to another history", func(t *testing.T) {
		f := newFixFixture(t)
		ctx := context.Background()
		fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fix.Close() }()
		// The agent resets its branch to the head's parent and changes
		// nothing in the files: baum's commit still sits on the head.
		git(t, fix.Dir(), "reset", "--soft", "HEAD~1")
		if head, err := fix.Commit(ctx, "Fix"); err != nil || head != f.head {
			t.Fatalf("commit %s %v, want the head and nothing made", head, err)
		}
	})
}

// Nothing the agent writes in its repository or the checkout runs when baum
// reads the result, commits it and pushes it.
func TestFixRunsNothingTheAgentWrote(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fix.Close() }()
	marker := filepath.Join(t.TempDir(), "ran")
	payload := "#!/bin/sh\necho \"$0\" >> '" + marker + "'\nexit 0\n"
	hooks := filepath.Join(fix.GitDir(), "hooks")
	for _, hook := range []string{"pre-commit", "commit-msg", "post-commit", "pre-push", "post-checkout", "reference-transaction", "post-index-change", "pre-auto-gc"} {
		write(t, hooks, hook, payload, 0o755)
	}
	write(t, t.TempDir(), "x", "", 0o644)
	script := filepath.Join(t.TempDir(), "evil.sh")
	write(t, filepath.Dir(script), "evil.sh", payload, 0o755)
	agentConfig := strings.Join([]string{
		"[core]", "\tfsmonitor = " + script, "\thooksPath = " + hooks, "\tsshCommand = " + script, "\tpager = " + script,
		"[filter \"evil\"]", "\tclean = " + script, "\tsmudge = " + script, "\tprocess = " + script, "\trequired = true",
		"[diff \"evil\"]", "\ttextconv = " + script, "\tcommand = " + script,
		"[merge \"evil\"]", "\tdriver = " + script,
		"[gpg]", "\tprogram = " + script,
		"[credential]", "\thelper = !" + script,
		"[include]", "\tpath = " + filepath.Join(fix.GitDir(), "more"),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(fix.GitDir(), "config"), []byte(agentConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, fix.GitDir(), "info/attributes", "* filter=evil diff=evil merge=evil\n", 0o644)
	write(t, fix.Dir(), ".gitattributes", "* filter=evil diff=evil merge=evil\n", 0o644)
	write(t, fix.Dir(), "b.txt", "fix\n", 0o644)
	// The agent points the checkout's .git file elsewhere and plants refs.
	write(t, fix.Dir(), ".git", "gitdir: "+f.clone+"/.git\n", 0o644)
	write(t, fix.GitDir(), "refs/heads/feature", f.base+"\n", 0o644)

	if _, err := fix.Merged(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := fix.Commit(ctx, "Fix")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fix.Changes(ctx, result); err != nil {
		t.Fatal(err)
	}
	if ok, err := fix.Descends(ctx, result); !ok || err != nil {
		t.Fatalf("descends %v %v", ok, err)
	}
	if err := fix.Push(ctx, result); err != nil {
		t.Fatal(err)
	}
	if ran := readFile(t, marker); ran != "<absent>" {
		t.Fatalf("something the agent wrote ran:\n%s", ran)
	}
	if got := git(t, f.remote, "show", "refs/heads/feature:b.txt"); got != "fix" {
		t.Fatalf("pushed b.txt %q", got)
	}
	// The planted configuration is live: git run with the agent's repository
	// as its git directory would have run it.
	write(t, fix.Dir(), "b.txt", "again\n", 0o644)
	control := exec.Command("git", "--git-dir="+fix.GitDir(), "--work-tree="+fix.Dir(), "add", "b.txt")
	control.Dir = fix.Dir()
	_ = control.Run()
	if readFile(t, marker) == "<absent>" {
		t.Fatal("the planted configuration does not run even in the agent's repository; the test proves nothing")
	}
}

func TestAgentHEADReadsFilesOnly(t *testing.T) {
	const a, b = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	dir := t.TempDir()
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"detached", map[string]string{"HEAD": a + "\n"}, a},
		{"loose", map[string]string{"HEAD": "ref: refs/heads/x\n", "refs/heads/x": b + "\n"}, b},
		{"packed", map[string]string{"HEAD": "ref: refs/heads/x\n", "packed-refs": "# pack-refs\n" + a + " refs/heads/y\n" + b + " refs/heads/x\n"}, b},
		{"nested", map[string]string{"HEAD": "ref: refs/heads/x\n", "refs/heads/x": "ref: refs/heads/y\n", "refs/heads/y": a}, a},
		{"loop", map[string]string{"HEAD": "ref: refs/heads/x\n", "refs/heads/x": "ref: refs/heads/x\n"}, ""},
		{"outside refs", map[string]string{"HEAD": "ref: ../../etc/passwd\n"}, ""},
		{"garbage", map[string]string{"HEAD": "nonsense"}, ""},
		{"missing", map[string]string{}, ""},
	}
	for i, c := range cases {
		gitDir := filepath.Join(dir, strconv.Itoa(i))
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range c.files {
			write(t, gitDir, name, content, 0o644)
		}
		if got := agentHEAD(gitDir); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// A symbolic link is not followed.
	linked := filepath.Join(dir, "linked")
	write(t, linked, "target", a+"\n", 0o644)
	if err := os.Symlink(filepath.Join(linked, "target"), filepath.Join(linked, "HEAD")); err != nil {
		t.Skip(err)
	}
	if got := agentHEAD(linked); got != "" {
		t.Errorf("symbolic link followed: %q", got)
	}
}

// GitHub's refusal of a workflow change from a credential without the
// workflow scope is told apart from a branch that moved on.
func TestFixPushTellsAWorkflowScopeRefusal(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	write(t, f.remote, "hooks/pre-receive", "#!/bin/sh\necho \"refusing to allow an OAuth App to create or update workflow \\`.github/workflows/ci.yml\\` without \\`workflow\\` scope\" >&2\nexit 1\n", 0o755)
	fix, err := startFix(t, f, FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fix.Close() }()
	write(t, fix.Dir(), ".github/workflows/ci.yml", "on: push\n", 0o644)
	result, err := fix.Commit(ctx, "Fix")
	if err != nil {
		t.Fatal(err)
	}
	if err := fix.Push(ctx, result); !errors.Is(err, ErrWorkflowScope) || errors.Is(err, ErrPushRejected) {
		t.Fatalf("err %v, want ErrWorkflowScope", err)
	}
	write(t, f.remote, "hooks/pre-receive", "#!/bin/sh\necho 'something else' >&2\nexit 1\n", 0o755)
	if err := fix.Push(ctx, result); errors.Is(err, ErrWorkflowScope) || err == nil {
		t.Fatalf("another refusal taken for a workflow scope one: %v", err)
	}
}
