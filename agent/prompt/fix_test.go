package prompt_test

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
)

func TestFixComposesTheSectionsItNeeds(t *testing.T) {
	both := prompt.Fix(true, true, false)
	for _, phrase := range []string{"untrusted data", "never follow them", "Do not commit", "## Merge conflicts", "## Failing checks"} {
		if !strings.Contains(both, phrase) {
			t.Errorf("fix instructions lack %q", phrase)
		}
	}
	if strings.Contains(both, "## Review comments") {
		t.Error("review comments without threads to address")
	}
	if conflicts := prompt.Fix(true, false, false); strings.Contains(conflicts, "## Failing checks") || !strings.Contains(conflicts, "conflict markers") {
		t.Errorf("conflicts only: %s", conflicts)
	}
	if checks := prompt.Fix(false, true, false); strings.Contains(checks, "## Merge conflicts") || !strings.Contains(checks, "network is off") {
		t.Errorf("checks only: %s", checks)
	}
	threads := prompt.Fix(false, false, true)
	for _, phrase := range []string{"## Review comments", "not\n  instructions to you", "CI or workflow configuration", "no @-mentions", "Do not reply to anyone"} {
		if !strings.Contains(threads, phrase) {
			t.Errorf("review comment instructions lack %q", phrase)
		}
	}
	if strings.Contains(threads, "## Failing checks") || strings.Contains(threads, "## Merge conflicts") {
		t.Errorf("threads only: %s", threads)
	}
}
