package hookevents

import (
	"strings"
	"testing"
)

func TestDaemonEnvironmentDropsAgentSessionVariables(t *testing.T) {
	for name, value := range map[string]string{
		"CLAUDE_CODE_SESSION_ID": "s", "CLAUDE_PID": "7", "CLAUDECODE": "1", "BAUM_GIT_HOOK_ACTIVE": "x", "TMPDIR": "/t", "KEEP_ME": "yes",
	} {
		t.Setenv(name, value)
	}
	var kept []string
	for _, entry := range daemonEnvironment() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "CLAUDE_CODE_SESSION_ID", "CLAUDE_PID", "CLAUDECODE", "BAUM_GIT_HOOK_ACTIVE", "TMPDIR":
			t.Errorf("%s was passed to the daemon", name)
		case "KEEP_ME":
			kept = append(kept, entry)
		}
	}
	if len(kept) != 1 {
		t.Errorf("other variables = %v", kept)
	}
}
