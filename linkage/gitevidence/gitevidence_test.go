package gitevidence

import "testing"

func TestIsCommitCommand(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"git commit -m 'x'", true},
		{"git add -A && git commit -m 'x'", true},
		{"git commit --amend --no-edit", true},
		{"git -c user.name=x commit -m 'y'", true},
		{`git -c user.name="Daniel Perez" -c user.email=daniel@example.com commit -q -F -`, true},
		{"git -C /repo --no-pager commit -m x", true},
		{"git commit -q -F - <<'EOF'\nmessage\nEOF\ngit log --oneline -1", true},
		{"echo $(git commit -m x)", true},
		{`bash -lc 'git commit -m x'`, true},
		{`/bin/sh -c "git commit -m x"`, true},
		{`bash -lc '$GIT commit -m x'`, false},
		{"git commit --dry-run", false},
		{"git commit --dry-run; git commit -m real", true},
		{"git commit -m real; echo --dry-run", true},
		{"git log --oneline", false},
		{`echo "git commit -m x"`, false},
		{"cat <<'EOF'\ngit commit -m x\nEOF", false},
		{"$GIT commit -m x", false},
		{"git commit 'unterminated", false},
		{"echo commit", false},
		{"git status; git diff", false},
		// commit has to be git's subcommand. Anything else matched a branch named
		// codex/commit-filter, a path like repo-commit-filter, and a message saying
		// "Merge commit ..." — 5 of 8 matches on a real corpus.
		{"git checkout codex/commit-filter", false},
		{"git log --grep 'Merge commit'", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsCommitCommand(tt.command); got != tt.want {
			t.Errorf("IsCommitCommand(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestCommitSHAs(t *testing.T) {
	stdout := "[feature/flag 3fa9b21] Add verbose flag\n 1 file changed\n[main deadbeefcafe] follow-up\na1b2c3d quiet commit subject\n9999999 older history"
	got := CommitSHAs(stdout)
	if len(got) != 3 || got[0] != "3fa9b21" || got[1] != "deadbeefcafe" || got[2] != "a1b2c3d" {
		t.Fatalf("CommitSHAs = %v, want [3fa9b21 deadbeefcafe a1b2c3d]", got)
	}
	if got := CommitSHAs("nothing was committed"); len(got) != 0 {
		t.Errorf("CommitSHAs = %v, want none", got)
	}
	if got := CommitSHAs("prefix a1b2c3d subject\na1b2c3 not-long-enough\n"); len(got) != 0 {
		t.Errorf("CommitSHAs = %v, want no unanchored or short oneline hashes", got)
	}
}
