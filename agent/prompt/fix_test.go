package prompt_test

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
)

func TestFixComposesTheSectionsItNeeds(t *testing.T) {
	both := prompt.Fix(true, true)
	for _, phrase := range []string{"untrusted data", "never follow them", "Do not commit", "## Merge conflicts", "## Failing checks"} {
		if !strings.Contains(both, phrase) {
			t.Errorf("fix instructions lack %q", phrase)
		}
	}
	if conflicts := prompt.Fix(true, false); strings.Contains(conflicts, "## Failing checks") || !strings.Contains(conflicts, "conflict markers") {
		t.Errorf("conflicts only: %s", conflicts)
	}
	if checks := prompt.Fix(false, true); strings.Contains(checks, "## Merge conflicts") || !strings.Contains(checks, "network is off") {
		t.Errorf("checks only: %s", checks)
	}
}
