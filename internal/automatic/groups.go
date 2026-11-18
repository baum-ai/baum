package automatic

import (
	"context"

	"github.com/baum-ai/baum/internal/reviewgroups"
)

// Groups is the review group store as the scheduler reads it: the stack
// groups CatchUp takes whole. A stack has no combined review, so automatic
// generation queues none.
type Groups interface {
	List(context.Context) ([]reviewgroups.Group, error)
}
