package generate

import (
	"context"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// SetViewRun replaces the compose stage's call of agent.ViewRun until the
// returned function restores it.
func SetViewRun(f func(context.Context, agent.ViewRequest) (agent.ViewOutcome, error)) func() {
	saved := viewRun
	viewRun = f
	return func() { viewRun = saved }
}

// SetProvisionalView replaces how a draft's provisional composition is made
// until the returned function restores it.
func SetProvisionalView(f func(*view.Review, *tiles.Document, []tiles.LibraryTile, map[string][]string) view.Composition) func() {
	saved := provisionalView
	provisionalView = f
	return func() { provisionalView = saved }
}
