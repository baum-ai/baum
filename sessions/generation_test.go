package sessions

import (
	"testing"

	"github.com/baumai/baum/sessions/session"
)

func TestIsGenerationRun(t *testing.T) {
	parent, spawn := "root", "spawn"
	graph := func(inputs ...session.UserInput) *session.Graph {
		return &session.Graph{
			Sessions:   []session.Session{{ID: "root"}, {ID: "child", ParentSessionID: &parent, SpawnedByToolUseID: &spawn}},
			UserInputs: inputs,
		}
	}
	for name, test := range map[string]struct {
		graph *session.Graph
		want  bool
	}{
		"marked first prompt": {graph(session.UserInput{SessionID: "root", Ordinal: 1, Text: "\n " + GenerationRunMarker + "\nReview the pull request."}), true},
		"marked after injected context": {graph(
			session.UserInput{SessionID: "root", Ordinal: 1, Text: "environment context"},
			session.UserInput{SessionID: "root", Ordinal: 2, Text: GenerationRunMarker + " Review."}), true},
		"marker only mentioned": {graph(session.UserInput{SessionID: "root", Ordinal: 1, Text: "why does " + GenerationRunMarker + " exist?"}), false},
		"marker in a subagent":  {graph(session.UserInput{SessionID: "child", Ordinal: 1, Text: GenerationRunMarker}), false},
		"ordinary session":      {graph(session.UserInput{SessionID: "root", Ordinal: 1, Text: "add the feature"}), false},
		"empty graph":           {&session.Graph{}, false},
		"nil graph":             {nil, false},
	} {
		if got := IsGenerationRun(test.graph); got != test.want {
			t.Errorf("%s: IsGenerationRun = %v, want %v", name, got, test.want)
		}
	}
}
