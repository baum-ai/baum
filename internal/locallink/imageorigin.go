package locallink

import (
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/sessions"
)

// ImagePromptResolver locates image inputs in the linked root conversation,
// sharing spawn ownership across the images of one session.
func ImagePromptResolver(s sessions.Session) func(string) (bundle.ImageOrigin, bool) {
	if s.Graph == nil {
		return func(string) (bundle.ImageOrigin, bool) { return bundle.ImageOrigin{}, false }
	}
	owners := newPromptOwners(s.Graph)
	return func(inputID string) (bundle.ImageOrigin, bool) {
		rootInput, ok := owners.of(inputID)
		if !ok {
			return bundle.ImageOrigin{}, false
		}
		input := owners.inputs[rootInput]
		if input.Ordinal < 1 {
			return bundle.ImageOrigin{}, false
		}
		origin := bundle.ImageOrigin{Session: s.ID, Turn: input.Ordinal, Input: rootInput}
		if rootInput != inputID {
			origin.ChildSession = owners.inputs[inputID].SessionID
			origin.ChildInput = inputID
		}
		return origin, true
	}
}
