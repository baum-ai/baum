package locallink

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/commitattribution"
	"github.com/baum-ai/baum/internal/fixdir"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// A fix agent's session, recorded in a checkout gone since, is linked to
// the commit baum made of its work through the observation the fix recorded,
// and its transcript and touched paths are read as the repository's.
func TestFixCommitLinksTheFixAgentsSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	fixRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { fixRoot = fixdir.Default })
	f := linktest.New(t)
	cmd := exec.Command("git", "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	cmd.Dir = f.Repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote: %s %v", out, err)
	}
	common, err := gitlocal.CommonDir(context.Background(), f.Repo)
	if err != nil {
		t.Fatal(err)
	}
	const fixSession = "ffffffff-0000-4000-8000-0000000000f1"
	checkout := filepath.Join(fixdir.ForClone(root, common), "job1", "checkout")
	f.AddEditorIn(t, fixSession, checkout, "10", "c.txt")

	// Without the fix's observation, editing the commit's files links nothing.
	if links := resolve(t, request(f)).Commits[1].Links; len(links) != 0 {
		t.Fatalf("second commit links = %+v before the fix recorded its commit", links)
	}

	o := commitattribution.Observation{Repository: linktest.Repository, CommitSHA: f.Commits[1], Agent: "claude_code", SessionID: fixSession,
		Kind: commitattribution.Commit, EventID: "fix:job1", At: time.Date(2026, 7, 1, 10, 40, 0, 0, time.UTC)}
	if err := (&commitattribution.Store{}).Record([]commitattribution.Observation{o}); err != nil {
		t.Fatal(err)
	}
	result := resolve(t, request(f))
	if got := linkedTo(result.Commits[1]); len(got) != 1 || !slices.Equal(got["commit_ref"], []string{fixSession}) {
		t.Fatalf("second commit links = %+v, want the fix agent's session", result.Commits[1].Links)
	}
	if result.Graphs[fixSession] == nil {
		t.Fatal("the fix agent's transcript was not read")
	}

	for _, s := range buildEvidence(t, evidenceRequest(t, f)) {
		if s.RootSessionID != fixSession {
			continue
		}
		encoded, _ := json.Marshal(s)
		if !strings.Contains(string(encoded), `"c.txt"`) || strings.Contains(string(encoded), "checkout") {
			t.Fatalf("fix session evidence does not place its edit in the repository:\n%s", encoded)
		}
		return
	}
	t.Fatal("evidence has no session for the fix agent")
}
