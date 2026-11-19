package jobs

import (
	"testing"

	"github.com/baum-ai/baum/agent"
)

// The transcript names the tile analysis's calls by what they write.
func TestCallNameOfTileCalls(t *testing.T) {
	for stage, want := range map[string]string{
		"fill-schema-change": "Library tile · Schema changes",
		"fill-api-change":    "Library tile · Interface changes",
		"fill-made-up":       "Library tile · made_up",
		agent.StageExhibits:  "Exhibits",
	} {
		if got := callName(agent.Request{Stage: stage}); got != want {
			t.Errorf("%s: %q, want %q", stage, got, want)
		}
	}
	if got := callName(agent.Request{Stage: agent.StageExhibits, Pass: 2}); got != "Exhibits · repair 1" {
		t.Errorf("repair: %q", got)
	}
}
