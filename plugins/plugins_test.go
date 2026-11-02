package plugins

import "testing"

func TestVersionReadsEachShippedManifest(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		if Version(agent) == "" {
			t.Errorf("no shipped version for %s", agent)
		}
	}
	if got := Version("other"); got != "" {
		t.Errorf("Version(other) = %q", got)
	}
}
