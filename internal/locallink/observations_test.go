package locallink

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/commitattribution"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

func TestHookCreationLinksWithoutTranscriptAndOtherEvidence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := linktest.New(t).NoSession(t)
	cmd := exec.Command("git", "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	cmd.Dir = f.Repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote: %s %v", out, err)
	}
	store := &commitattribution.Store{}
	o := commitattribution.Observation{Repository: linktest.Repository, CommitSHA: f.Commits[0], Agent: "claude-code", SessionID: linktest.SessionID, Kind: commitattribution.Commit, EventID: "post-commit", At: time.Date(2026, 7, 1, 10, 30, 0, 0, time.UTC)}
	if err := store.Record([]commitattribution.Observation{o}); err != nil {
		t.Fatal(err)
	}
	// No timing, file overlap or readable transcript is needed for a hook-owned
	// creation. The unrelated commit must retain an empty link list.
	result := resolve(t, request(f))
	if len(result.Sessions) != 1 || result.Sessions[0].ID != o.SessionID || len(result.Commits[0].Links) != 1 || result.Commits[0].Links[0].Method != "commit_ref" || len(result.Commits[1].Links) != 0 {
		t.Fatalf("hook result: %+v", result)
	}
	if result.Graphs[o.SessionID] != nil {
		t.Fatal("invented a transcript")
	}
	repo, err := gitlocal.Open(context.Background(), f.Repo, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	commits, err := repo.Commits(context.Background(), f.Base, f.Head)
	_ = repo.Close()
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := BuildSessionEvidence(context.Background(), EvidenceRequest{Dir: f.Repo, Commits: commits, Sessions: f.Store})
	if err != nil || len(evidence) != 1 || evidence[0].RootSessionID != o.SessionID || len(evidence[0].Prompts) != 0 {
		t.Fatalf("metadata-only bundle: %+v %v", evidence, err)
	}
	links, err := LinkCommits(context.Background(), f.Repo, commits, LinkOptions{Sessions: f.Store})
	if err != nil || len(links) != 1 || links[0].Commit != o.CommitSHA {
		t.Fatalf("feature links: %+v %v", links, err)
	}
}

func TestPushAndOtherRepositoriesDoNotCreateLinks(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := linktest.New(t).NoSession(t)
	cmd := exec.Command("git", "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	cmd.Dir = f.Repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote: %s %v", out, err)
	}
	for _, kind := range []string{commitattribution.Push, commitattribution.Commit} {
		repository := linktest.Repository
		if kind == commitattribution.Commit {
			repository = "another/widgets"
		}
		o := commitattribution.Observation{Repository: repository, CommitSHA: f.Commits[0], Agent: "claude-code", SessionID: linktest.SessionID, Kind: kind, EventID: kind, At: time.Now()}
		if err := (&commitattribution.Store{}).Record([]commitattribution.Observation{o}); err != nil {
			t.Fatal(err)
		}
	}
	if result := resolve(t, request(f)); len(result.Sessions) != 0 || len(result.Commits[0].Links) != 0 {
		t.Fatalf("unproven creation: %+v", result)
	}
}

func TestHookCreationSuppliesReviewBundleEvidence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := linktest.New(t)
	cmd := exec.Command("git", "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	cmd.Dir = f.Repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote: %s %v", out, err)
	}
	// Replace the transcript's commit-creation call with a read-only command.
	err := filepath.WalkDir(f.Store.ClaudeProjectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b = []byte(strings.ReplaceAll(string(b), "git commit -m feature", "git status"))
		b = []byte(strings.ReplaceAll(string(b), f.Commits[0], strings.Repeat("c", 40)))
		return os.WriteFile(path, b, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	o := commitattribution.Observation{Repository: linktest.Repository, CommitSHA: f.Commits[0], Agent: "claude-code", SessionID: linktest.SessionID, Kind: commitattribution.Commit, EventID: "post-commit", At: time.Now()}
	if err := (&commitattribution.Store{}).Record([]commitattribution.Observation{o}); err != nil {
		t.Fatal(err)
	}
	repo, err := gitlocal.Open(context.Background(), f.Repo, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	commits, err := repo.Commits(context.Background(), f.Base, f.Head)
	_ = repo.Close()
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := BuildSessionEvidence(context.Background(), EvidenceRequest{Dir: f.Repo, Commits: commits, Sessions: f.Store, MaxPromptsPerSession: 5})
	if err != nil || len(evidence) != 1 || evidence[0].RootSessionID != o.SessionID || evidence[0].Confidence < 0.9 {
		t.Fatalf("bundle: %+v %v", evidence, err)
	}
}

func TestHookCreationOutranksTranscriptCreationGuesses(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := linktest.New(t)
	cmd := exec.Command("git", "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	cmd.Dir = f.Repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote: %s %v", out, err)
	}
	o := commitattribution.Observation{Repository: linktest.Repository, CommitSHA: f.Commits[0], Agent: "claude-code", SessionID: "observed-creator", Kind: commitattribution.Commit, EventID: "post-commit", At: time.Now()}
	if err := (&commitattribution.Store{}).Record([]commitattribution.Observation{o}); err != nil {
		t.Fatal(err)
	}
	result := resolve(t, request(f))
	if len(result.Commits[0].Links) != 1 || result.Commits[0].Links[0].Session != o.SessionID || result.Commits[0].Links[0].Method != "commit_ref" {
		t.Fatalf("creation: %+v", result.Commits[0])
	}
}
