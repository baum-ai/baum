package jobs

import (
	"context"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/commitattribution"
	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/github"
)

// The commit a fix pushed is recorded as made by its agent's session, under
// the session store's name for the agent; a run that reported no session, or
// a backend with no session store, records nothing.
func TestRecordFixCommitNamesTheAgentsSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	l := &Local{Attributions: &commitattribution.Store{}}
	repository := github.Repository{Host: "github.com", Owner: "Acme", Name: "Widgets"}
	const (
		claudeCommit = "1111111111111111111111111111111111111111"
		codexCommit  = "2222222222222222222222222222222222222222"
		noSession    = "3333333333333333333333333333333333333333"
		otherBackend = "4444444444444444444444444444444444444444"
		session      = "aaaaaaaa-0000-4000-8000-000000000001"
	)
	l.recordFixCommit(repository, fix.Outcome{Commit: claudeCommit, Agent: &agent.Result{SessionID: session}}, agent.BackendClaude, "job1")
	l.recordFixCommit(repository, fix.Outcome{Commit: codexCommit, Agent: &agent.Result{SessionID: session}}, agent.BackendCodex, "job2")
	l.recordFixCommit(repository, fix.Outcome{Commit: noSession, Agent: &agent.Result{}}, agent.BackendClaude, "job3")
	l.recordFixCommit(repository, fix.Outcome{Commit: otherBackend, Agent: &agent.Result{SessionID: session}}, "cursor", "job4")

	got, err := l.Attributions.Lookup(context.Background(), []string{"acme/widgets"}, []string{claudeCommit, codexCommit, noSession, otherBackend})
	if err != nil {
		t.Fatal(err)
	}
	agents := map[string]string{}
	for _, o := range got {
		if o.Kind != commitattribution.Commit || o.SessionID != session {
			t.Fatalf("observation = %+v", o)
		}
		agents[o.CommitSHA] = o.Agent
	}
	if len(agents) != 2 || agents[claudeCommit] != "claude_code" || agents[codexCommit] != "codex" {
		t.Fatalf("recorded agents by commit = %v", agents)
	}
}
