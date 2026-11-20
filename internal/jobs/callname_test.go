package jobs

import (
	"testing"

	"github.com/baum-ai/baum/agent"
)

// The transcript names a review's calls by their stage, and its repairs.
func TestCallName(t *testing.T) {
	for stage, want := range map[string]string{
		"":                 "Review",
		agent.StageOutline: "Outline",
		agent.StageDetails: "Details",
		agent.StageContext: "Codebase context",
		"tile-tl_a":        "tile-tl_a",
	} {
		if got := callName(agent.Request{Stage: stage}); got != want {
			t.Errorf("%s: %q, want %q", stage, got, want)
		}
	}
	if got := callName(agent.Request{Stage: agent.StageDetails, Pass: 2}); got != "Details · repair 1" {
		t.Errorf("repair: %q", got)
	}
}
