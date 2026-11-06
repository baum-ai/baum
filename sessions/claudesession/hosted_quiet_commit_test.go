package claudesession

import (
	"strconv"
	"testing"

	"github.com/baum-ai/baum/sessions/session"
)

// Failure diagnostics reject the following log even when the enclosing shell
// succeeds. Successful newline-separated logs retain creation evidence.
func TestHostedFailedQuietCommitIsNotMarkedCommitted(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	outcome := func(command, output string) session.Outcome {
		graph, _ := hostedBuildClaudeLines(t,
			`{"parentUuid":null,"isSidechain":false,"cwd":"/repo","gitBranch":"main","type":"user","uuid":"user-1","sessionId":"`+id+`","timestamp":"2026-08-06T10:00:00Z","promptId":"prompt-1","message":{"role":"user","content":"Commit"}}`,
			`{"parentUuid":"user-1","isSidechain":false,"cwd":"/repo","gitBranch":"main","type":"assistant","uuid":"assistant-1","sessionId":"`+id+`","timestamp":"2026-08-06T10:00:01Z","message":{"id":"message-1","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":`+strconv.Quote(command)+`}}]}}`,
			`{"parentUuid":"assistant-1","isSidechain":false,"cwd":"/repo","gitBranch":"main","type":"user","uuid":"user-2","sessionId":"`+id+`","timestamp":"2026-08-06T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":`+strconv.Quote(output)+`}]},"toolUseResult":{"stdout":`+strconv.Quote(output)+`,"stderr":""}}`,
		)
		return graph.Root().Outcome
	}
	for _, separator := range []string{"\n", " && "} {
		command := "git commit -q -m x" + separator + "git log --oneline -1"
		if got := outcome(command, "nothing to commit, working tree clean\na1b2c3d older subject"); got == session.OutcomeCommitted {
			t.Errorf("outcome = %q after a failed commit with separator %q", got, separator)
		}
		if got := outcome(command, "a1b2c3d new subject"); got != session.OutcomeCommitted {
			t.Errorf("outcome = %q after a successful commit with separator %q", got, separator)
		}
	}
}
