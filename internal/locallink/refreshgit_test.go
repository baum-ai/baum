package locallink

import (
	"context"
	"testing"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/workstats"
)

// maxUnchangedRefreshGit bounds the git processes a refresh of a clone whose
// checkpoints did not move runs: the Entire refs (for-each-ref), the remotes'
// URLs (config), and the worktrees (rev-parse and worktree list) twice, for
// discovery and for the touched-path bounds. None depends on how many
// checkpoints or commits there are.
const maxUnchangedRefreshGit = 6

// Two refreshes of an unchanged clone whose commits name an Entire
// checkpoint: the second lists, dates and reads no checkpoint again, and
// opens no private repository of its own, since the caller's is shared.
// Each refresh starts with an empty evidence memo, as if a transcript had
// changed, so it loads sessions.
func TestRefreshOfAnUnchangedCloneRunsBoundedGit(t *testing.T) {
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer).NoSession(t)
	f.AddCheckpoint(t)
	repo, err := gitlocal.Open(context.Background(), f.Repo, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	req := evidenceRequest(t, f)
	req.Repo = repo

	refresh := func() map[string]int64 {
		t.Helper()
		useEvidenceMemo(t)
		before := workstats.Take()
		buildEvidence(t, req)
		moved := map[string]int64{}
		for _, delta := range workstats.Take().Since(before) {
			moved[delta.Name] = delta.Count
		}
		return moved
	}
	first := refresh()
	if first["git.archive"] == 0 || first["git.ls-tree"] == 0 {
		t.Fatalf("the first refresh read no checkpoint: %v", first)
	}
	second := refresh()
	if second["sessions.load.calls"] != 1 {
		t.Fatalf("the second refresh did not load sessions: %v", second)
	}
	for _, command := range []string{"git.archive", "git.ls-tree", "git.log", "git.cat-file", "git.merge-base"} {
		if second[command] != 0 {
			t.Errorf("the second refresh ran %s %d times", command, second[command])
		}
	}
	// Opening a private repository runs git config, as reading the remotes'
	// URLs does once.
	if second["git.config"] > 1 {
		t.Errorf("the second refresh opened a private repository of its own: %v", second)
	}
	if got := second["git.processes"]; got > maxUnchangedRefreshGit || got >= first["git.processes"] {
		t.Errorf("the second refresh ran %d git processes (the first %d), want at most %d: %v",
			got, first["git.processes"], maxUnchangedRefreshGit, second)
	}
	t.Logf("git processes: first refresh %d, second %d", first["git.processes"], second["git.processes"])
}
