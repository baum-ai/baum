package gitlocal

import (
	"context"
	"testing"
	"time"
)

func TestAuthoredCommitsListsOneAuthorsBranchCommitsOnce(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "config", "user.email", "test@example.com")
	// A commit on two branches is listed once; another author's is not; a
	// merge is not.
	git(t, f.dir, "branch", "copy", "feature")
	t.Setenv("GIT_AUTHOR_EMAIL", "jim.test@example.com")
	write(t, f.dir, "other.txt", "other\n", 0o644)
	other := commit(t, f.dir, "someone else")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	git(t, f.dir, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	r := open(t, f.dir, Options{})
	email, err := r.UserEmail(context.Background())
	if err != nil || email != "test@example.com" {
		t.Fatalf("email = %q, %v", email, err)
	}
	commits, err := r.AuthoredCommits(context.Background(), time.Now().Add(-time.Hour), email)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, c := range commits {
		seen[c.OID]++
		if c.OID == other || len(c.Parents) > 1 {
			t.Errorf("listed %s %q", c.OID, c.Subject)
		}
	}
	if len(commits) != 2 || seen[f.base] != 1 || seen[f.head] != 1 {
		t.Fatalf("commits = %+v", commits)
	}
	if later, err := r.AuthoredCommits(context.Background(), time.Now().Add(time.Hour), email); err != nil || len(later) != 0 {
		t.Errorf("future window = %v, %v", later, err)
	}
	if _, err := r.AuthoredCommits(context.Background(), time.Now(), ""); err == nil {
		t.Error("an empty address was accepted")
	}
}

func TestUserEmailUnset(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	r := open(t, dir, Options{})
	if email, err := r.UserEmail(context.Background()); err != nil || email != "" {
		t.Fatalf("email = %q, %v", email, err)
	}
}
