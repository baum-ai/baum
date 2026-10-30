package claudesession

import (
	"strconv"
	"testing"

	"github.com/baum-ai/baum/sessions/session"
)

// A quiet commit that fails leaves the previous HEAD on `git log --oneline`
// when the log is not chained with `&&`, so that line is not a created commit.
func TestHostedFailedQuietCommitIsNotMarkedCommitted(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	outcome := func(command string) session.Outcome {
		graph, _ := hostedBuildClaudeLines(t,
			`{"parentUuid":null,"isSidechain":false,"cwd":"/repo","gitBranch":"main","type":"user","uuid":"user-1","sessionId":"`+id+`","timestamp":"2026-08-06T10:00:00Z","promptId":"prompt-1","message":{"role":"user","content":"Commit"}}`,
			`{"parentUuid":"user-1","isSidechain":false,"cwd":"/repo","gitBranch":"main","type":"assistant","uuid":"assistant-1","sessionId":"`+id+`","timestamp":"2026-08-06T10:00:01Z","message":{"id":"message-1","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":`+strconv.Quote(command)+`}}]}}`,
			`{"parentUuid":"assistant-1","isSidechain":false,"cwd":"/repo","gitBranch":"main","type":"user","uuid":"user-2","sessionId":"`+id+`","timestamp":"2026-08-06T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"nothing to commit, working tree clean\na1b2c3d older subject"}]},"toolUseResult":{"stdout":"nothing to commit, working tree clean\na1b2c3d older subject","stderr":""}}`,
		)
		return graph.Root().Outcome
	}
	if got := outcome("git commit -q -m x\ngit log --oneline -1"); got == session.OutcomeCommitted {
		t.Errorf("outcome = %q for a newline-separated log, want not committed", got)
	}
	if got := outcome("git commit -q -m x && git log --oneline -1"); got != session.OutcomeCommitted {
		t.Errorf("outcome = %q for an && chained log, want committed", got)
	}
}
