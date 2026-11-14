package generate

import (
	"context"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/tiles"
)

// SetComposeRun replaces the compose stage's call of agent.ComposeRun until
// the returned function restores it.
func SetComposeRun(f func(context.Context, agent.ComposeRequest) (agent.ComposeOutcome, error)) func() {
	saved := composeRun
	composeRun = f
	return func() { composeRun = saved }
}

// SetProvisionalLayout replaces how a draft's provisional layout is made
// until the returned function restores it.
func SetProvisionalLayout(f func(*tiles.Document, []string, *tiles.Composition) (tiles.Composition, error)) func() {
	saved := provisionalLayout
	provisionalLayout = f
	return func() { provisionalLayout = saved }
}
