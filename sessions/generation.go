package sessions

import (
	"strings"

	"github.com/baumai/baum/sessions/session"
)

// GenerationRunMarker starts the prompt of every agent run baum itself starts
// to generate a review. Such a run is recorded in the same stores as the
// developer's own sessions, in a worktree of the same clone, so the marker is
// what keeps it from being taken as evidence about the pull request it
// reviews.
const GenerationRunMarker = "<!-- baum:generation-run -->"

// IsGenerationRun reports whether graph is one of baum's own generation runs:
// a user input of its top-level session begins with GenerationRunMarker. A
// prompt that only mentions the marker further in does not count.
func IsGenerationRun(graph *session.Graph) bool {
	root := Root(graph)
	if root == nil {
		return false
	}
	for _, input := range graph.UserInputs {
		if input.SessionID == root.ID && strings.HasPrefix(strings.TrimSpace(input.Text), GenerationRunMarker) {
			return true
		}
	}
	return false
}
